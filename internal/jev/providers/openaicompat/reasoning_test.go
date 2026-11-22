package openaicompat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// reasoningModel is an OpenAI reasoning model: it refuses max_tokens, a
// temperature other than 1 and logprobs, each with its own 400, in that
// order, and otherwise answers text.
func reasoningModel(t *testing.T, text func(n int) string) func(int, seen) (int, any) {
	return func(n int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.","param":"max_tokens"}}`
		}
		if temp, ok := r.Raw["temperature"]; ok && temp != 1.0 {
			return 400, `{"error":{"message":"Unsupported value: 'temperature' does not support 0 with this model. Only the default (1) value is supported.","param":"temperature"}}`
		}
		if _, ok := r.Raw["logprobs"]; ok {
			return 400, `{"error":{"message":"This model does not support logprobs","param":"logprobs"}}`
		}
		if r.Body.MaxCompletionTokens != completionBudget {
			t.Errorf("max_completion_tokens = %d, want %d", r.Body.MaxCompletionTokens, completionBudget)
		}
		return 200, textReply(text(n))
	}
}

// A model that refuses max_tokens gets max_completion_tokens, one that
// refuses temperature gets none, and only the logprobs refusal makes the
// backend sample. Each is learned once: the next question sends no request
// it would refuse.
func TestAReasoningModelIsAnsweredBySampling(t *testing.T) {
	f := newFake(t, reasoningModel(t, func(int) string { return "B" }))
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Probability != jev.ProbSampled || resp.Answers["q"].Choice != "second" {
		t.Fatalf("%+v %v", resp, err)
	}
	// Three refusals, then five samples.
	if n := len(f.requests()); n != 8 {
		t.Fatalf("%d requests, want 8", n)
	}
	for _, r := range f.requests()[3:] {
		if _, ok := r.Raw["temperature"]; ok {
			t.Errorf("a sample sent temperature: %v", r.Raw)
		}
	}
	if _, err := ask(t, cl, choiceOf("first", "second")); err != nil || len(f.requests()) != 13 {
		t.Fatalf("not remembered: %v (%d requests)", err, len(f.requests()))
	}

	// Probe takes the same path: the provider is found to sample.
	f = newFake(t, reasoningModel(t, func(int) string { return "Yes" }))
	cl, _ = newClient(t, f.URL, "", jev.Capabilities{})
	caps, err := cl.Backend.(Prober).Probe(context.Background())
	if err != nil || caps.Probability != jev.ProbSampled {
		t.Fatalf("probe = %+v %v", caps, err)
	}
}

// A model that refuses only max_tokens keeps its log-probabilities.
func TestAModelThatRefusesMaxTokensKeepsLogprobs(t *testing.T) {
	answer := oracle(t, "second", 0)
	f := newFake(t, func(n int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			return 400, `{"error":{"message":"max_tokens is not supported, use max_completion_tokens"}}`
		}
		return answer(n, r)
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Probability != jev.ProbLogprob || resp.Answers["q"].Choice != "second" || len(f.requests()) != 2 {
		t.Fatalf("%+v %v (%d requests)", resp, err, len(f.requests()))
	}
	if r := f.requests()[1]; r.Body.MaxCompletionTokens != completionBudget || r.Raw["temperature"] != 0.0 {
		t.Errorf("the retry: %v", r.Raw)
	}
}

// With room for more than one token an answer counts only when, trimmed, it
// is exactly an allowed label: anything longer is a miss.
func TestALongerAnswerMustBeExactlyALabel(t *testing.T) {
	texts := []string{"B", " B\n", "B. The second one.", "b", "A"}
	f := newFake(t, reasoningModel(t, func(n int) string { return texts[(n-3)%len(texts)] }))
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil {
		t.Fatal(err)
	}
	// 2 × B, 1 × A and 2 misses: B = max((2+1)/(5+2), 1/2), just above A.
	if a := resp.Answers["q"]; a.Choice != "second" || !near(a.Probabilities["second"], 0.5) || a.Probabilities["first"] >= a.Probabilities["second"] {
		t.Errorf("answer %+v", a)
	}

	f = newFake(t, reasoningModel(t, func(int) string { return "I would pick B." }))
	cl, _ = newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("first", "second")); !errors.Is(err, jev.ErrInvalidAnswer) || strings.Contains(err.Error(), "pick") {
		t.Fatalf("err = %v, want a miss that quotes no model text", err)
	}
}

// A 400 that names a parameter already adjusted is not retried again.
func TestARepeatedRefusalIsNotRetriedForever(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) { return 400, `{"error":"max_tokens: invalid value"}` })
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if _, err := ask(t, cl, choiceOf("first", "second")); err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("err = %v", err)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d requests, want 3 (the refusal, its adjusted retry, one sample)", n)
	}
}
