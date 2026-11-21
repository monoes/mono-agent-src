package openaicompat

import (
	"fmt"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

func optionIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("opt-%03d", i)
	}
	return ids
}

// spread answers with labels[i] getting p[i] (the rest of the labels nothing).
func spread(labels []string, p ...float64) map[string]any {
	alts := make([]alt, 0, len(p))
	for i, x := range p {
		alts = append(alts, alt{labels[i], x})
	}
	return logprobReply(alts...)
}

// A torn model: the group it drilled into holds no option more probable
// than the even share of a smaller group it did not, so that group is
// answered too and the answer is an option the model judged.
func TestATornHierarchyAnswersAnOptionItJudged(t *testing.T) {
	ids := optionIDs(44) // 15 groups at k=20: fourteen of 3, the last of 2
	f := newFake(t, func(_ int, r seen) (int, any) {
		labels, _ := labelsOf(r.user())
		switch len(labels) {
		case 15:
			p := make([]float64, 15)
			for i := range p {
				p[i] = 0.41 / 13
			}
			p[0], p[14] = 0.30, 0.29
			return 200, spread(labels, p...)
		case 3:
			return 200, spread(labels, 1.0/3, 1.0/3, 1.0/3)
		case 2:
			return 200, spread(labels, 0.9, 0.1)
		}
		t.Errorf("unexpected request with %d labels", len(labels))
		return 500, `{}`
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf(ids...))
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Answers["q"]
	if a.Choice != "opt-042" || !near(a.Probabilities["opt-042"], 0.29*0.9) || !near(a.Probabilities["opt-000"], 0.1) {
		t.Fatalf("answer = %q %v", a.Choice, a.Probabilities)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d requests, want the groups, group 0 and the last group", n)
	}
}

// A server that returns fewer alternatives than the request was planned for
// (Fireworks: 5) has the question planned again, once, with the k it showed.
// A probe's finding, stored as MaxChoiceOptions, plans for it from the start.
func TestAQuestionIsReplannedForFewerAlternatives(t *testing.T) {
	ids := optionIDs(10)
	reply := func(t *testing.T) func(int, seen) (int, any) {
		target := oracle(t, "opt-007", 5)
		return func(n int, r seen) (int, any) {
			if labels, _ := labelsOf(r.user()); len(labels) > 2 {
				// Torn across the labels, and only five alternatives of them.
				return 200, spread(labels, 0.08, 0.08, 0.08, 0.08, 0.08)
			}
			return target(n, r)
		}
	}
	f := newFake(t, reply(t))
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf(ids...))
	if err != nil || resp.Answers["q"].Choice != "opt-007" {
		t.Fatalf("%+v %v", resp, err)
	}
	if c := cl.Backend.Capabilities(); c.MaxChoiceOptions != 2 {
		t.Fatalf("caps = %+v", c)
	}
	if labels, _ := labelsOf(f.requests()[0].user()); len(labels) != 10 {
		t.Fatalf("first request offers %d labels", len(labels))
	}

	stored := newFake(t, reply(t))
	cl, _ = newClient(t, stored.URL, "", jev.Capabilities{Probability: jev.ProbLogprob, MaxChoiceOptions: 2})
	if resp, err := ask(t, cl, choiceOf(ids...)); err != nil || resp.Answers["q"].Choice != "opt-007" {
		t.Fatalf("%+v %v", resp, err)
	}
	for i, r := range stored.requests() {
		if labels, _ := labelsOf(r.user()); len(labels) != 2 || (i == 0 && r.Body.TopLogprobs != 7) {
			t.Fatalf("request %d offers %d labels, asks %d alternatives", i, len(labels), r.Body.TopLogprobs)
		}
	}
}
