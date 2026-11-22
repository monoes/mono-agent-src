package anthropic

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestTheKindIsRegistered(t *testing.T) {
	if !slices.Contains(jevconf.Kinds(), Kind) {
		t.Fatalf("kinds %v lack %q", jevconf.Kinds(), Kind)
	}
}

// Every question type, end to end through jev.Client.Ask on a client
// jevconf built from a saved provider record.
func TestEveryKindEndToEnd(t *testing.T) {
	f := newFake(t, func(_ int, r messagesRequest) (int, string) {
		e := enum(r)
		switch {
		case slices.Equal(e, []string{"yes", "no"}):
			return toolReply("yes")
		case slices.Equal(e, []string{"0", "1", "2"}):
			return toolReply("2")
		case slices.Equal(e, []string{"billing", "sales", "support"}):
			return toolReply("billing")
		}
		return http.StatusBadRequest, `{"error":"unexpected enum"}`
	})
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{"ticket": "refund please"}, map[string]jev.Question{
		"route":   choice("billing", "sales", "support"),
		"urgent":  noul("Is it urgent?"),
		"quality": {Type: jev.TypeScore, Instructions: "How clear is it?", Criteria: []string{"unclear", "ok", "clear"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if a := resp.Answers["route"]; a.Choice != "billing" || !near(a.Probabilities["billing"], 0.75) || !near(a.Confidence, 0.625) {
		t.Errorf("route: %+v", a)
	}
	if a := resp.Answers["urgent"]; !near(a.Noul, 6.0/7) {
		t.Errorf("urgent: %+v", a)
	}
	if a := resp.Answers["quality"]; !near(a.Score, 1.625) || a.Legend["2"] != "clear" {
		t.Errorf("quality: %+v", a)
	}
	if resp.Provider != "claude" || resp.Probability != jev.ProbSampled || resp.Model != DefaultModel {
		t.Errorf("provider %q probability %q model %q", resp.Provider, resp.Probability, resp.Model)
	}
	// Every question takes all 5 samples, the two-option noul too (plan O2).
	got := f.requests()
	if len(got) != 15 {
		t.Fatalf("%d requests, want 15", len(got))
	}
	if resp.Usage.InputTokens != 150 || resp.Usage.OutputTokens != 30 {
		t.Errorf("usage %+v", resp.Usage)
	}
	for _, s := range got {
		h := s.header
		if s.method != http.MethodPost || s.path != "/v1/messages" || h.Get("X-Api-Key") != testKey ||
			h.Get("Anthropic-Version") != "2023-06-01" || h.Get("Content-Type") != "application/json" || h.Get("Authorization") != "" {
			t.Fatalf("request %s %s headers %v", s.method, s.path, h)
		}
		b := s.body
		if b.Model != DefaultModel || b.Temperature != 1 || b.MaxTokens != maxTokens || b.ToolChoice != (toolChoice{"tool", "answer"}) {
			t.Fatalf("body %+v", b)
		}
		if !strings.Contains(b.System, "untrusted_") || len(b.Messages) != 1 || !strings.Contains(b.Messages[0].Content, `{"ticket":"refund please"}`) {
			t.Fatalf("prompt %q / %+v", b.System, b.Messages)
		}
	}
}

func TestTheRecordsModelAndTheCallsModel(t *testing.T) {
	f := newFake(t, sequence("yes", "yes", "yes", "yes", "yes"))
	c := providerClient(t, f.URL, testKey, "claude-own-model")
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": noul("ok?")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "claude-own-model" || f.requests()[0].body.Model != "claude-own-model" {
		t.Errorf("model %q / %q", resp.Model, f.requests()[0].body.Model)
	}
}

func TestSampleFrequenciesAreTheProbabilities(t *testing.T) {
	f := newFake(t, sequence("a", "a", "b", "a", "c"))
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": choice("a", "b", "c")})
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Answers["q"]
	if a.Choice != "a" || !near(a.Probabilities["a"], 0.5) || !near(a.Probabilities["b"], 0.25) || !near(a.Probabilities["c"], 0.25) {
		t.Errorf("answer %+v", a)
	}
	if want := (0.5 - 1.0/3) / (1 - 1.0/3); !near(a.Confidence, want) {
		t.Errorf("confidence %v, want %v", a.Confidence, want)
	}
}

// No question stops early, and samples are smoothed: p(o) = (count(o)+1) / (5+n), so even a unanimous five is not certain.
func TestEveryQuestionTakesAllSamples(t *testing.T) {
	cases := []struct {
		name    string
		q       jev.Question
		answers []string
		calls   int
		check   func(jev.Answer) bool
	}{
		{"unanimous noul", noul("ok?"), []string{"no", "no", "no", "no", "no"}, 5, func(a jev.Answer) bool { return near(a.Noul, 1.0/7) }},
		{"split noul", noul("ok?"), []string{"yes", "no", "yes", "yes", "no"}, 5, func(a jev.Answer) bool { return near(a.Noul, 4.0/7) }},
		{"a miss among the first three", noul("ok?"), []string{"", "yes", "yes", "yes", "yes"}, 5, func(a jev.Answer) bool { return near(a.Noul, 5.0/7) }},
		{"two-option choice", choice("x", "y"), []string{"y", "y", "y", "y", "y"}, 5, func(a jev.Answer) bool { return a.Choice == "y" && near(a.Probabilities["y"], 6.0/7) }},
		{"three agreeing, then a dissent", choice("x", "y"), []string{"y", "y", "y", "x", "y"}, 5, func(a jev.Answer) bool { return a.Choice == "y" && near(a.Probabilities["y"], 5.0/7) }},
		{"three options", choice("x", "y", "z"), []string{"y", "y", "y", "y", "y"}, 5, func(a jev.Answer) bool { return a.Choice == "y" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, sequence(tc.answers...))
			c := providerClient(t, f.URL, testKey, "")
			resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": tc.q})
			if err != nil {
				t.Fatal(err)
			}
			if n := len(f.requests()); n != tc.calls {
				t.Errorf("%d calls, want %d", n, tc.calls)
			}
			if !tc.check(resp.Answers["q"]) {
				t.Errorf("answer %+v", resp.Answers["q"])
			}
		})
	}
}

// An answer outside the enum, or no tool call, is a miss; fewer than three
// allowed answers in five fail, and the model's text is never quoted.
func TestAnswersOutsideTheEnumAreMisses(t *testing.T) {
	f := newFake(t, sequence("a", "zzz-not-offered", "", "b", "a"))
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": choice("a", "b", "c")})
	if err != nil {
		t.Fatal(err)
	}
	if a := resp.Answers["q"]; a.Choice != "a" || math.Abs(a.Probabilities["a"]-0.375) > 1e-9 || a.Probabilities["b"] >= a.Probabilities["a"] || !near(a.Probabilities["c"], 0.25) {
		t.Errorf("answer %+v", a)
	}

	f = newFake(t, sequence("zzz-not-offered", "", "a", "zzz-not-offered", "a"))
	c = providerClient(t, f.URL, testKey, "")
	_, err = c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": choice("a", "b", "c")})
	if !errors.Is(err, jev.ErrInvalidAnswer) {
		t.Fatalf("want ErrInvalidAnswer, got %v", err)
	}
	if strings.Contains(err.Error(), "zzz") || strings.Contains(err.Error(), "I think") {
		t.Errorf("the error quotes the model: %v", err)
	}
}

// Option ids that cannot be their own label are asked as A, B, …, AA, ….
func TestLetterLabels(t *testing.T) {
	for i, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA", 701: "ZZ", 702: "AAA"} {
		if got := letters(i); got != want {
			t.Errorf("letters(%d) = %q, want %q", i, got, want)
		}
	}
	ids := make([]string, 30)
	for i := range ids {
		ids[i] = "option number " + string(rune('a'+i%26)) + strings.Repeat("!", i/26)
	}
	q := choice(ids...)
	sorted := jev.OrderedOptionIDs(q)
	f := newFake(t, sequence("AD", "AD", "AD", "AD", "AD"))
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": q})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Answers["q"].Choice; got != sorted[29] {
		t.Errorf("choice %q, want %q", got, sorted[29])
	}
	e := enum(f.requests()[0].body)
	if len(e) != 30 || e[0] != "A" || e[29] != "AD" {
		t.Errorf("enum %v", e)
	}
	if !strings.Contains(f.requests()[0].body.Messages[0].Content, "- AD: the "+sorted[29]+" option") {
		t.Errorf("prompt lacks the option text: %s", f.requests()[0].body.Messages[0].Content)
	}
}

// Ids that are plain tokens are their own labels.
func TestIDsAreTheirOwnLabels(t *testing.T) {
	q := choice("route.billing", "route_sales", "Route-3")
	p, err := prepare(q)
	if err != nil {
		t.Fatal(err)
	}
	want := jev.OrderedOptionIDs(q)
	if !slices.Equal(p.labels, want) {
		t.Errorf("labels %v, want %v", p.labels, want)
	}
	long := choice(strings.Repeat("x", 65), "b")
	if p, _ := prepare(long); !slices.Equal(p.labels, []string{"A", "B"}) {
		t.Errorf("a 65-character id kept as a label: %v", p.labels)
	}
}

func TestNoulCriteriaAreDescribed(t *testing.T) {
	q := jev.Question{Type: jev.TypeNoul, Instructions: "Spam?", Criteria: map[string]any{"true": "it is spam", "false": "it is mail"}}
	p, err := prepare(q)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.text, "- yes: it is spam") || !strings.Contains(p.text, "- no: it is mail") {
		t.Errorf("text %q", p.text)
	}
	keys := make([]string, 0)
	for k, v := range p.keys {
		keys = append(keys, k+"="+v)
	}
	sort.Strings(keys)
	if !slices.Equal(keys, []string{"no=false", "yes=true"}) {
		t.Errorf("keys %v", keys)
	}
}
