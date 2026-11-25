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
