package openaicompat

import (
	"context"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
)

// chosenOnly is a completion that carries only the chosen token's logprob
// (an empty top_logprobs): p is the model's probability of text.
func chosenOnly(text string, p float64) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message":  map[string]any{"role": "assistant", "content": text},
			"logprobs": map[string]any{"content": []any{map[string]any{"token": text, "logprob": math.Log(p), "top_logprobs": []any{}}}},
		}},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 1},
	}
}

// The reviewers' reproduction: a server that returns only the chosen token's
// logprob (model P(Yes) = 0.5) once read as noul 1.0. One alternative is no
// distribution: the question is sampled, five samples cannot read 0.9, and
// the backend's k is not lowered to 1.
func TestASingleAlternativeIsSampledNotCertain(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) { return 200, chosenOnly("Yes", 0.5) })
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, jev.Question{Type: jev.TypeNoul, Instructions: "x?"})
	if err != nil {
		t.Fatal(err)
	}
	if n := resp.Answers["q"].Noul; n >= 0.9 || resp.Probability != jev.ProbSampled {
		t.Fatalf("noul = %v, probability %s", n, resp.Probability)
	}
	if n := len(f.requests()); n != 1+samples {
		t.Fatalf("%d requests, want the logprobs one and %d samples", n, samples)
	}
	if c := cl.Backend.Capabilities(); c.Probability != jev.ProbSampled || c.OptionsPerRequest != defaultTopK-labelSlack {
		t.Fatalf("caps = %+v", c)
	}
	// A choice of four is asked flat, not as a binary hierarchy.
	before := len(f.requests())
	f.setReply(func(int, seen) (int, any) { return 200, textReply("C") })
	resp, err = ask(t, cl, choiceOf("a", "b", "c", "d"))
	if err != nil || resp.Answers["q"].Choice != "c" || resp.Answers["q"].Probabilities["c"] >= 0.9 {
		t.Fatalf("%+v %v", resp, err)
	}
	for _, r := range f.requests()[before:] {
		if labels, _ := labelsOf(r.user()); len(labels) != 4 {
			t.Fatalf("a request offers %d labels", len(labels))
		}
	}

	// Probe sees the same and stores no k of 1.
	f = newFake(t, func(int, seen) (int, any) { return 200, chosenOnly("Yes", 0.5) })
	cl, _ = newClient(t, f.URL, "", jev.Capabilities{})
	caps, err := cl.Backend.(Prober).Probe(context.Background())
	if err != nil || caps.Probability != jev.ProbSampled || caps.OptionsPerRequest < 2 || caps.OptionsPerRequest != defaultTopK-labelSlack {
		t.Fatalf("probe = %+v %v", caps, err)
	}
}

// A response with two alternatives is read; k never drops below them.
func TestLearnKIgnoresFewerThanTwo(t *testing.T) {
	b := &Backend{topK: defaultTopK}
	b.learnK(1, defaultTopK)
	b.learnK(0, defaultTopK)
	if b.k() != defaultTopK {
		t.Fatalf("k = %d", b.k())
	}
	b.learnK(2, defaultTopK)
	if b.k() != 2 || b.perRequest() != 2 {
		t.Fatalf("k = %d, per request %d", b.k(), b.perRequest())
	}
}

// The reviewers' reproduction: A = 0.5, "I" = 0.45 (no label), B = 0.05 once
// gated at 0.909. The answer keeps its raw mass.
func TestMassOnNonLabelTokensStaysOffTheAnswer(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		return 200, logprobReply(alt{"A", 0.5}, alt{"I", 0.45}, alt{"B", 0.05})
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("x", "y"))
	if err != nil {
		t.Fatal(err)
	}
	a := resp.Answers["q"]
	id, p := jev.GateValue(resp, a)
	if id != "x" || p > 0.5+1e-9 || resp.Probability != jev.ProbLogprob {
		t.Fatalf("gate = %s %v (%s)", id, p, resp.Probability)
	}
	if !near(a.Probabilities["x"]+a.Probabilities["y"], 1) {
		t.Fatalf("probabilities = %v", a.Probabilities)
	}
	// The same for a noul: the answer is a weak yes, not a 0.9 one.
	f.setReply(func(int, seen) (int, any) {
		return 200, logprobReply(alt{"Yes", 0.5}, alt{"I", 0.45}, alt{"No", 0.05})
	})
	resp, err = ask(t, cl, jev.Question{Type: jev.TypeNoul, Instructions: "x?"})
	if err != nil || resp.Answers["q"].Noul > 0.5+1e-9 || resp.Answers["q"].Noul < 0.5-1e-9 {
		t.Fatalf("noul %+v %v", resp, err)
	}
}

// In a hierarchy, the group level and the member level each keep only
// their raw mass: 0.6 on the group, 0.6 on the member → 0.36. A group of
// one member that got the off-label rest does not become the answer.
func TestAHierarchyKeepsOffLabelMassOffTheAnswer(t *testing.T) {
	ids := optionIDs(44)
	f := newFake(t, func(_ int, r seen) (int, any) {
		labels, _ := labelsOf(r.user())
		alts := []alt{{labels[0], 0.6}, {"Hmm", 0.39}, {labels[1], 0.01}}
		for i := 0; len(alts) < r.Body.TopLogprobs; i++ {
			alts = append(alts, alt{fmt.Sprintf("~%d", i), 1e-12})
		}
		return 200, logprobReply(alts...)
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf(ids...))
	if err != nil {
		t.Fatal(err)
	}
	id, p := jev.GateValue(resp, resp.Answers["q"])
	if id != "opt-000" || !near(p, 0.36) {
		t.Fatalf("gate = %s %v", id, p)
	}

	// The same with a server of five alternatives: three levels, no flip.
	f = newFake(t, func(_ int, r seen) (int, any) {
		labels, _ := labelsOf(r.user())
		return 200, logprobReply(alt{labels[0], 0.6}, alt{"Hmm", 0.39}, alt{labels[1], 0.01})
	})
	cl, _ = newClient(t, f.URL, "", jev.Capabilities{})
	if resp, err = ask(t, cl, choiceOf(ids...)); err != nil {
		t.Fatal(err)
	}
	if id, p := jev.GateValue(resp, resp.Answers["q"]); id != "opt-000" || p > 0.36 {
		t.Fatalf("gate = %s %v", id, p)
	}
}

// The reviewers' reproduction: 3 of 5 samples say A, 2 are junk. Dividing by
// the valid samples once made A 1.0; it is (3+1)/(5+2) now.
func TestJunkSamplesCountTowardTheSamplesAsked(t *testing.T) {
	f := newFake(t, func(n int, r seen) (int, any) {
		if n%5 < 3 {
			return 200, textReply("A")
		}
		return 200, textReply("Sure")
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{Probability: jev.ProbSampled})
	resp, err := ask(t, cl, choiceOf("x", "y"))
	if err != nil {
		t.Fatal(err)
	}
	id, p := jev.GateValue(resp, resp.Answers["q"])
	if id != "x" || !near(p, 4.0/7) || resp.Probability != jev.ProbSampled {
		t.Fatalf("gate = %s %v (%s)", id, p, resp.Probability)
	}
}

// The samples of one question are sent at once: the fake answers none until
// all five are in flight.
func TestTheSamplesOfAQuestionRunConcurrently(t *testing.T) {
	var mu sync.Mutex
	inFlight := 0
	all := make(chan struct{})
	f := newFake(t, func(int, seen) (int, any) {
		mu.Lock()
		inFlight++
		if inFlight == samples {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
			return 200, textReply("A")
		case <-time.After(5 * time.Second):
			return 500, `{"error":{"code":"sequential"}}`
		}
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{Probability: jev.ProbSampled})
	cl.Retries = 0
	if _, err := ask(t, cl, choiceOf("x", "y")); err != nil {
		t.Fatal(err)
	}
}

// Probability is sampled when any question of the request was sampled,
// though another was read from log-probabilities.
func TestOneSampledQuestionMakesTheResponseSampled(t *testing.T) {
	f := newFake(t, func(_ int, r seen) (int, any) {
		if labels, _ := labelsOf(r.user()); len(labels) == 3 || !r.Body.Logprobs {
			return 200, textReply("A") // no logprobs for the three-option question
		}
		return 200, logprobReply(alt{"A", 0.9}, alt{"B", 0.1})
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := cl.Ask(context.Background(), nil, map[string]jev.Question{"two": choiceOf("x", "y"), "three": choiceOf("p", "q", "r")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Probability != jev.ProbSampled {
		t.Fatalf("probability = %s", resp.Probability)
	}
}
