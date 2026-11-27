package openaicompat

import (
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// directClient asks the adapter at url through jev.Client, with no saved
// provider record.
func directClient(t *testing.T, url string, caps jev.Capabilities) *jev.Client {
	t.Helper()
	b, err := New(jevconf.Provider{BaseURL: url, Model: testModel, Caps: caps}, "")
	if err != nil {
		t.Fatal(err)
	}
	return &jev.Client{Provider: "local", Backend: b, Model: testModel}
}

// sampler is a server without log-probabilities whose every sample names the
// option target (or, in a request over groups, the group holding it), except
// that the first sample of a request offering dissent labels names another.
func sampler(t *testing.T, target string, dissent int) func(int, seen) (int, any) {
	var mu sync.Mutex
	done := false
	return func(_ int, r seen) (int, any) {
		labels, lines := labelsOf(r.user())
		pick := ""
		for _, l := range labels {
			if l == target || strings.Contains(lines[l], target) {
				pick = l
				break
			}
		}
		if pick == "" {
			t.Errorf("%q is in no label of %q", target, r.user())
			return 500, `{}`
		}
		mu.Lock()
		defer mu.Unlock()
		if len(labels) == dissent && !done {
			done = true
			for _, l := range labels {
				if l != pick {
					return 200, textReply(l)
				}
			}
		}
		return 200, textReply(pick)
	}
}

// A sampled answer records what a unanimous result would read on its question:
// five agreeing samples pass any threshold of a surface that takes the lowering, one dissent in any request on the
// way to the answer passes nothing it did not before.
func TestASampledAnswerRecordsItsUnanimousProbability(t *testing.T) {
	sampled := jev.Capabilities{Probability: jev.ProbSampled}
	for _, c := range []struct {
		name    string
		options int
		target  int
		dissent int // the label count of the request with one other answer; 0 = unanimous
		want    float64
	}{
		{"two options", 2, 1, 0, 6.0 / 7},
		{"two options, 4 of 5", 2, 1, 2, 6.0 / 7},
		{"fifteen options, one request", 15, 7, 0, 6.0 / 20},
		{"fifteen options, 4 of 5", 15, 7, 15, 6.0 / 20},
		// 44 options: 15 groups, fourteen of 3 and the last of 2.
		{"hierarchy", 44, 20, 0, (6.0 / 20) * (6.0 / 8)},
		{"hierarchy, a dissent over the groups", 44, 20, 15, (6.0 / 20) * (6.0 / 8)},
		{"hierarchy, a dissent in the group", 44, 20, 3, (6.0 / 20) * (6.0 / 8)},
		{"hierarchy, the smaller last group", 44, 43, 0, (6.0 / 20) * (6.0 / 7)},
		// 29 options: 14 groups of 2 and one of 1, which is not asked.
		{"hierarchy, a group of one", 29, 28, 0, 6.0 / 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			ids := optionIDs(c.options)
			f := newFake(t, sampler(t, ids[c.target], c.dissent))
			cl := directClient(t, f.URL, sampled)
			resp, err := ask(t, cl, choiceOf(ids...))
			if err != nil {
				t.Fatal(err)
			}
			a := resp.Answers["q"]
			_, p := jev.GateValue(resp, a)
			if a.Choice != ids[c.target] || !near(a.Unanimous, c.want) {
				t.Fatalf("answer %q p=%v unanimous=%v, want %q and %v", a.Choice, p, a.Unanimous, ids[c.target], c.want)
			}
			unanimous := c.dissent == 0
			if unanimous != near(p, c.want) || (!unanimous && p >= c.want) {
				t.Fatalf("p = %v, a unanimous result reads %v", p, c.want)
			}
			if got := p >= jevconf.GateThreshold(jevconf.Inbox, resp, a, 0.9); got != unanimous {
				t.Errorf("at 0.9: passes %v (p=%v)", got, p)
			}
		})
	}
}

// Noul and score questions record theirs too.
func TestSampledNoulAndScoreRecordTheirUnanimousProbability(t *testing.T) {
	sampled := jev.Capabilities{Probability: jev.ProbSampled}
	for _, c := range []struct {
		reply string
		q     jev.Question
		want  float64
	}{
		{"No", jev.Question{Type: jev.TypeNoul, Instructions: "x?"}, 6.0 / 7},
		{"2", jev.Question{Type: jev.TypeScore, Instructions: "x?", Criteria: []string{"a", "b", "c"}}, 6.0 / 8},
	} {
		f := newFake(t, func(int, seen) (int, any) { return 200, textReply(c.reply) })
		cl := directClient(t, f.URL, sampled)
		resp, err := ask(t, cl, c.q)
		if err != nil || !near(resp.Answers["q"].Unanimous, c.want) {
			t.Errorf("%s: %+v %v, want %v", c.q.Type, resp, err, c.want)
		}
	}
}

// An answer read from log-probabilities, at any level, has no ceiling: its
// threshold is as configured.
func TestALogprobAnswerRecordsNoUnanimousProbability(t *testing.T) {
	ids := optionIDs(44)
	f := newFake(t, oracle(t, ids[1], 0))
	cl := directClient(t, f.URL, jev.Capabilities{})
	for _, c := range []struct {
		n, target int
	}{{3, 1}, {44, 20}} {
		f.setReply(oracle(t, ids[c.target], 0))
		resp, err := ask(t, cl, choiceOf(ids[:c.n]...))
		if err != nil || resp.Probability != jev.ProbLogprob || resp.Answers["q"].Unanimous != 0 {
			t.Fatalf("%d options: %+v %v", c.n, resp, err)
		}
	}
	// The group from log-probabilities, its members sampled.
	groups, words := oracle(t, ids[20], 0), sampler(t, ids[20], 0)
	f = newFake(t, func(n int, r seen) (int, any) {
		if labels, _ := labelsOf(r.user()); len(labels) == 15 {
			return groups(n, r)
		}
		return words(n, r)
	})
	cl = directClient(t, f.URL, jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf(ids...))
	if err != nil || resp.Probability != jev.ProbSampled || resp.Answers["q"].Unanimous != 0 {
		t.Fatalf("mixed: %+v %v", resp, err)
	}
}
