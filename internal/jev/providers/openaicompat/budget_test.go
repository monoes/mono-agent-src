package openaicompat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// A host that caps top_logprobs at 5 and refuses 20 with a 400 keeps its
// log-probabilities: the request is asked again at k = 5, and k = 5 is kept.
func TestA400ToTwentyAlternativesRetriesWithFive(t *testing.T) {
	answer := oracle(t, "second", 0)
	f := newFake(t, func(n int, r seen) (int, any) {
		if r.Body.TopLogprobs > retryTopK {
			return 400, `{"error":{"message":"top_logprobs must be at most 5","code":"invalid_value"}}`
		}
		return answer(n, r)
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Probability != jev.ProbLogprob || resp.Answers["q"].Choice != "second" || len(f.requests()) != 2 {
		t.Fatalf("%+v %v (%d requests)", resp, err, len(f.requests()))
	}
	if _, err := ask(t, cl, choiceOf("first", "second")); err != nil || len(f.requests()) != 3 || f.requests()[2].Body.TopLogprobs != retryTopK {
		t.Fatalf("k = 5 not kept: %v (%d requests)", err, len(f.requests()))
	}

	// Probe finds the same.
	f = newFake(t, func(n int, r seen) (int, any) {
		if r.Body.TopLogprobs > retryTopK {
			return 400, `{"error":{"message":"top_logprobs must be at most 5"}}`
		}
		return 200, logprobReply(alt{"Yes", 0.9}, alt{"No", 0.1})
	})
	cl, _ = newClient(t, f.URL, "", jev.Capabilities{})
	caps, err := cl.Backend.(Prober).Probe(context.Background())
	if err != nil || caps.Probability != jev.ProbLogprob || caps.OptionsPerRequest != 2 {
		t.Fatalf("probe = %+v %v", caps, err)
	}
}

// A reasoning model that spends all of max_completion_tokens 16 thinking
// answers nothing: that request is asked once more with 256, which is kept;
// the answer must still be exactly a label.
func TestAnEmptyAnswerGetsTheLargerBudgetOnce(t *testing.T) {
	f := newFake(t, func(n int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens'. Use 'max_completion_tokens' instead."}}`
		}
		if _, ok := r.Raw["logprobs"]; ok {
			return 400, `{"error":{"message":"logprobs are not supported"}}`
		}
		r2 := textReply("")
		if r.Body.MaxCompletionTokens == largeCompletionBudget {
			r2 = textReply("B")
		}
		r2["usage"] = map[string]any{"prompt_tokens": 10, "completion_tokens": r.Body.MaxCompletionTokens}
		return 200, r2
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Answers["q"].Choice != "second" || resp.Probability != jev.ProbSampled {
		t.Fatalf("%+v %v", resp, err)
	}
	var small, large int
	for _, r := range f.requests() {
		if _, ok := r.Raw["logprobs"]; ok {
			continue // the refused logprobs requests
		}
		switch r.Body.MaxCompletionTokens {
		case completionBudget:
			small++
		case largeCompletionBudget:
			large++
		}
	}
	// The first sample (alone) tries 16, then 256; the other four ask 256 at once.
	if small != 1 || large != samples {
		t.Fatalf("%d requests at 16, %d at 256", small, large)
	}
	if resp.Usage.OutputTokens != completionBudget+samples*largeCompletionBudget {
		t.Fatalf("usage %+v: the tokens of the empty answer are billed too", resp.Usage)
	}
}

// A thinking model under a one-token limit answers its first reasoning token:
// no label, almost no mass on any. The error names the cause, quotes nothing
// the model said, and nothing is asked again.
func TestAThinkingModelGetsAClearError(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		return 200, logprobReply(alt{"<think>", 0.995}, alt{"Okay", 0.004}, alt{"A", 0.0005}, alt{"B", 0.0005})
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	_, err := ask(t, cl, choiceOf("first", "second"))
	if !errors.Is(err, jev.ErrInvalidAnswer) || !strings.Contains(err.Error(), "reasoning/thinking model") ||
		strings.Contains(err.Error(), "think>") || strings.Contains(err.Error(), "Okay") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("%d requests, want 1", n)
	}
}
