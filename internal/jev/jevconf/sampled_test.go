package jevconf

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// surfaceDecisions names EVERY surface and says whether a unanimous sample
// lowers its threshold. Only the low-risk ones do (owner decision,
// 2026-11-26): a new surface belongs here as false unless the owner says
// otherwise.
var surfaceDecisions = map[Surface]bool{
	Inbox:       true, // a label on a message
	Capture:     true, // a suggested route
	PeopleLinks: true, // a suggested link
	Retry:       true, // retry triage
	APIAuto:     true, // the auto model's pick

	ActionFallback: false, // clicks and types: action fallback, record replay, the social bots
	HIL:            false, // auto_decide approves and rejects
	PeopleReview:   false,
	Asks:           false, // links a reply to an ask, which resumes a run
	Decider:        false, // decides an org's approvals, at every level
	CLI:            false,
	DynamicOrg:     false,

	NodeSurface("ai.choose"):             false, // a branch may lead to a send
	NodeSurface("ai.decide"):             false, // a branch may lead to a send
	NodeSurface("browser.jev"):           false, // types plain and vault values
	NodeSurface("applications.evaluate"): false,
}

// sampledAnswer is the answer a sampling adapter builds from counts of asked
// samples on a question of k options: the add-one smoothed shares, with the
// probability a unanimous result would read.
func sampledAnswer(t *testing.T, k, asked int, counts map[string]int) jev.Answer {
	t.Helper()
	ids := make([]string, k)
	crit := map[string]any{}
	for i := range ids {
		ids[i] = fmt.Sprintf("o%03d", i)
		crit[ids[i]] = "option"
	}
	a, err := jev.AnswerFrom(jev.Question{Type: jev.TypeChoice, Criteria: crit}, jev.SampledProbs(counts, asked, ids))
	if err != nil {
		t.Fatal(err)
	}
	a.Unanimous = jev.UnanimousProbability(asked, k)
	return a
}

// A surface lowers a threshold only when it is on the one list, and a surface
// nobody listed does not.
func TestOnlyTheListedSurfacesLowerASampledThreshold(t *testing.T) {
	sampled := &jev.Response{Probability: jev.ProbSampled}
	unanimous := sampledAnswer(t, 2, 5, map[string]int{"o000": 5})
	_, p := jev.GateValue(sampled, unanimous)

	all := map[Surface]bool{"a_new_surface": false, "": false, NodeSurface("a.new_node"): false}
	for s, lowered := range surfaceDecisions {
		all[s] = lowered
	}
	for s, lowered := range all {
		if got := LowersSampled(s); got != lowered {
			t.Errorf("%q: LowersSampled = %v, want %v", s, got, lowered)
		}
		th := GateThreshold(s, sampled, unanimous, 0.9)
		if lowered && !(p >= th && th < 0.9) {
			t.Errorf("%q: a unanimous %v does not pass the threshold in force %v", s, p, th)
		}
		if !lowered && (th != 0.9 || SampledCeiling(s, sampled, unanimous) != 0) {
			t.Errorf("%q: threshold %v, want the configured 0.9", s, th)
		}
	}
	var listed []Surface
	for _, s := range Surfaces {
		if surfaceDecisions[s] {
			listed = append(listed, s)
		}
	}
	if got := SampledLoweredSurfaces(); !slices.Equal(got, listed) {
		t.Errorf("SampledLoweredSurfaces = %v, want %v", got, listed)
	}
	if len(sampledLowered) != len(listed) {
		t.Errorf("the list holds %v: every entry must be a surface of Surfaces pinned as lowered (%v)", sampledLowered, listed)
	}
}

// For every number of options, a unanimous result passes whatever threshold
// is configured, and anything less passes exactly the thresholds it passed
// before the sampled rule, no more.
func TestOnlyAUnanimousSampleGainsFromTheSampledThreshold(t *testing.T) {
	sampled := &jev.Response{Probability: jev.ProbSampled}
	for _, n := range []int{3, 5, 20} {
		for k := 2; k <= 255; k++ {
			configured := []float64{0, 0.05, 0.3, 0.5, 0.6, 0.75, 0.8, float64(n+1) / float64(n+k), 0.9, 1}
			all := sampledAnswer(t, k, n, map[string]int{"o000": n})
			if _, p := jev.GateValue(sampled, all); !near(p, float64(n+1)/float64(n+k)) {
				t.Fatalf("n=%d k=%d: unanimous reads %v, want %d/%d", n, k, p, n+1, n+k)
			}
			less := []jev.Answer{}
			for agree := 1; agree < n; agree++ {
				// The others named another option, or no option at all.
				less = append(less,
					sampledAnswer(t, k, n, map[string]int{"o000": agree, "o001": n - agree}),
					sampledAnswer(t, k, n, map[string]int{"o000": agree}))
			}
			for _, c := range configured {
				_, p := jev.GateValue(sampled, all)
				if th := GateThreshold(Inbox, sampled, all, c); p < th || th > c {
					t.Fatalf("n=%d k=%d configured %v: unanimous %v against %v", n, k, c, p, th)
				}
				for i, a := range less {
					_, p := jev.GateValue(sampled, a)
					if got, before := p >= GateThreshold(Inbox, sampled, a, c), p >= c; got != before {
						t.Fatalf("n=%d k=%d configured %v, case %d: p=%v passes=%v, before the rule %v", n, k, c, i, p, got, before)
					}
				}
			}
		}
	}
}

// A threshold the sampled answer can reach stays what the user configured.
func TestAReachableThresholdIsUnchanged(t *testing.T) {
	sampled := &jev.Response{Probability: jev.ProbSampled}
	a := sampledAnswer(t, 2, 5, map[string]int{"o000": 5})
	for _, c := range []float64{0, 0.5, 0.7, 0.85} {
		if got := GateThreshold(Inbox, sampled, a, c); got != c {
			t.Errorf("configured %v: threshold %v", c, got)
		}
	}
	if got := GateThreshold(Inbox, sampled, a, 0.9); !near(got, 6.0/7) {
		t.Errorf("an unreachable 0.9 on two options: %v, want 6/7", got)
	}
	three := sampledAnswer(t, 3, 5, map[string]int{"o000": 5})
	if got := GateThreshold(Inbox, sampled, three, 0.9); !near(got, 0.75) {
		t.Errorf("an unreachable 0.9 on three options: %v, want 0.75", got)
	}
	// Four of five on two options reads 5/7: it passed 0.7 before and still does, never 0.8 or 0.9.
	four := sampledAnswer(t, 2, 5, map[string]int{"o000": 4, "o001": 1})
	_, p := jev.GateValue(sampled, four)
	for c, want := range map[float64]bool{0.7: true, 0.8: false, 0.9: false, 1: false} {
		if got := p >= GateThreshold(Inbox, sampled, four, c); got != want {
			t.Errorf("4 of 5 at %v: passes %v", c, got)
		}
	}
}

// Only a sampled response, and only an answer its adapter marked, has a
// lowered threshold: log-probabilities keep the configured one.
func TestOtherProbabilityKindsKeepTheirThreshold(t *testing.T) {
	marked := sampledAnswer(t, 2, 5, map[string]int{"o000": 5})
	for _, resp := range []*jev.Response{nil, {}, {Probability: jev.ProbNative}, {Probability: jev.ProbLogprob},
		{Probability: jev.ProbSoftmax}, {Probability: jev.ProbNone}} {
		if got := GateThreshold(Inbox, resp, marked, 0.9); got != 0.9 {
			t.Errorf("%+v: threshold %v", resp, got)
		}
		if got := SampledCeiling(Inbox, resp, marked); got != 0 {
			t.Errorf("%+v: ceiling %v", resp, got)
		}
	}
	// A sampled response may hold an answer that was read from
	// log-probabilities: it is not marked.
	unmarked := marked
	unmarked.Unanimous = 0
	if got := GateThreshold(Inbox, &jev.Response{Probability: jev.ProbSampled}, unmarked, 0.9); got != 0.9 {
		t.Errorf("unmarked answer: threshold %v", got)
	}
	if got := CapThreshold(0.9, 0); got != 0.9 {
		t.Errorf("no ceiling: %v", got)
	}
}

func TestUnanimousProbability(t *testing.T) {
	for _, c := range []struct {
		n, k int
		want float64
	}{{5, 2, 6.0 / 7}, {5, 3, 0.75}, {5, 15, 0.3}, {20, 2, 21.0 / 22}, {0, 2, 0}, {5, 0, 0}} {
		if got := jev.UnanimousProbability(c.n, c.k); !near(got, c.want) {
			t.Errorf("UnanimousProbability(%d, %d) = %v, want %v", c.n, c.k, got, c.want)
		}
	}
}

// The ceiling is the adapter's alone: it is never encoded, and a server that
// names it sets nothing.
func TestNoServerCanSetTheCeiling(t *testing.T) {
	raw, err := json.Marshal(sampledAnswer(t, 2, 5, map[string]int{"o000": 5}))
	if err != nil || strings.Contains(strings.ToLower(string(raw)), "unanimous") {
		t.Fatalf("%s %v", raw, err)
	}
	var a jev.Answer
	if err := json.Unmarshal([]byte(`{"type":"noul","noul":0.2,"unanimous":0.1,"Unanimous":0.1}`), &a); err != nil || a.Unanimous != 0 {
		t.Fatalf("%+v %v", a, err)
	}
}
