package openaicompat

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/operator"
)

// asOperator clears every agent marker, so a test run inside an agent saves
// records as the operator's own terminal does.
func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
}

// reasoningHost refuses max_tokens, temperature and more than 5 alternatives,
// as separate 400s, and otherwise knows the answer.
func reasoningHost(t *testing.T) *fake {
	answer := oracle(t, "second", 0)
	return newFake(t, func(i int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens'."}}`
		}
		if _, ok := r.Raw["temperature"]; ok {
			return 400, `{"error":{"message":"Unsupported parameter: 'temperature'."}}`
		}
		if r.Body.TopLogprobs > retryTopK {
			return 400, `{"error":{"message":"top_logprobs must be at most 5"}}`
		}
		return answer(i, r)
	})
}

// askNew asks one question through a new client of the server at url, and
// reports the requests it took.
func askNew(t *testing.T, f *fake, model string) []seen {
	t.Helper()
	before := len(f.requests())
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	if model != "" {
		cl.Model = model
	}
	resp, err := ask(t, cl, choiceOf("first", "second"))
	if err != nil || resp.Probability != jev.ProbLogprob || resp.Answers["q"].Choice != "second" {
		t.Fatalf("%+v %v", resp, err)
	}
	return f.requests()[before:]
}

// A new client of the same server and model sends what an earlier one
// learned: no refusal is collected twice.
func TestARefusalIsLearnedOnceForAServerAndModel(t *testing.T) {
	asOperator(t)
	f := reasoningHost(t)
	if first := askNew(t, f, ""); len(first) < 4 {
		t.Fatalf("the first client took %d requests: the host refused nothing", len(first))
	}
	again := askNew(t, f, "")
	if len(again) != 1 {
		t.Fatalf("a second client took %d requests, want 1", len(again))
	}
	r := again[0]
	if _, ok := r.Raw["temperature"]; ok || r.Body.MaxTokens != 0 || r.Body.MaxCompletionTokens != completionBudget || r.Body.TopLogprobs != retryTopK {
		t.Errorf("sent %v", r.Raw)
	}
}

// What one model refused says nothing about another on the same server.
func TestAnotherModelIsAskedAfresh(t *testing.T) {
	asOperator(t)
	f := reasoningHost(t)
	askNew(t, f, "")
	other := askNew(t, f, "m2")
	if _, ok := other[0].Raw["max_tokens"]; !ok || other[0].Body.TopLogprobs != defaultTopK {
		t.Errorf("another model's first request: %v", other[0].Raw)
	}
}

// What was learned is forgotten after quirksTTL, so a server that changed is
// asked afresh.
func TestARefusalIsForgottenAfterAWhile(t *testing.T) {
	asOperator(t)
	now := time.Now()
	quirksNow = func() time.Time { return now }
	t.Cleanup(func() { quirksNow = time.Now })
	f := reasoningHost(t)
	askNew(t, f, "")
	now = now.Add(quirksTTL - time.Second)
	if got := askNew(t, f, ""); len(got) != 1 {
		t.Fatalf("within quirksTTL: %d requests, want 1", len(got))
	}
	now = now.Add(2 * time.Second)
	if _, ok := askNew(t, f, "")[0].Raw["max_tokens"]; !ok {
		t.Error("after quirksTTL the old refusal still shapes the request")
	}
}

// Questions asked at once all send max_tokens before any refusal is back:
// each is sent again as it was but for that parameter. None takes its refusal
// for one of top_logprobs, which would cut the alternatives it asks for.
func TestQuestionsInFlightTogetherAllSurviveARefusedParameter(t *testing.T) {
	asOperator(t)
	const n = 4
	answer := oracle(t, "second", 0)
	var arrived atomic.Int32
	all := make(chan struct{})
	f := newFake(t, func(i int, r seen) (int, any) {
		if _, ok := r.Raw["max_tokens"]; ok {
			if arrived.Add(1) == n {
				close(all)
			}
			select {
			case <-all:
			case <-time.After(5 * time.Second):
			}
			return 400, `{"error":{"message":"Unsupported parameter: 'max_tokens'. Use 'max_completion_tokens' instead."}}`
		}
		return answer(i, r)
	})
	cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
	qs := map[string]jev.Question{}
	for i := range n {
		qs["q"+strconv.Itoa(i)] = choiceOf("first", "second")
	}
	resp, err := cl.Ask(context.Background(), map[string]any{"ticket": "refund please"}, qs)
	if err != nil || len(resp.Answers) != n || len(f.requests()) != 2*n {
		t.Fatalf("%+v %v (%d requests)", resp, err, len(f.requests()))
	}
	for i, r := range f.requests() {
		if r.Body.TopLogprobs != defaultTopK {
			t.Errorf("request %d asks for %d alternatives, want %d", i, r.Body.TopLogprobs, defaultTopK)
		}
	}
}
