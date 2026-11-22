package anthropic

import (
	"context"
	"net/http"
	"testing"
)

// The client's retry budget reaches the backend: 0 is one attempt, 1 one
// retry, the default two, for questions and for the models list.
func TestTheClientsRetryBudgetIsHonoured(t *testing.T) {
	for _, budget := range []int{0, 1, 2} {
		f := newFake(t, func(int, messagesRequest) (int, string) {
			return http.StatusServiceUnavailable, `{"type":"error","error":{"type":"overloaded_error"}}`
		})
		c := providerClient(t, f.URL, testKey, "")
		if budget != 2 {
			c.Retries = budget
		}
		if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err == nil {
			t.Fatalf("budget %d: a 503 succeeded", budget)
		}
		if n := len(f.requests()); n != budget+1 {
			t.Errorf("budget %d: %d attempts", budget, n)
		}
		if _, err := c.Models(context.Background()); err == nil || len(f.requests()) != 2*(budget+1) {
			t.Errorf("budget %d: models: %v after %d requests", budget, err, len(f.requests()))
		}
	}
}
