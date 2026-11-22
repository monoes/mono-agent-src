package openaicompat

import (
	"context"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
)

// The client's retry budget reaches the backend: 0 is one attempt, 1 one
// retry, the default two, for questions and for the models list.
func TestTheClientsRetryBudgetIsHonoured(t *testing.T) {
	noWait := func(_ *fake, h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "0")
			h.ServeHTTP(w, r)
		})
	}
	for _, budget := range []int{0, 1, 2} {
		f := newFake(t, func(int, seen) (int, any) { return 503, `{"error":{"code":"overloaded"}}` }, noWait)
		cl, _ := newClient(t, f.URL, "", jev.Capabilities{})
		if budget != 2 {
			cl.Retries = budget
		}
		if _, err := ask(t, cl, choiceOf("x", "y")); err == nil {
			t.Fatalf("budget %d: a 503 succeeded", budget)
		}
		if n := len(f.requests()); n != budget+1 {
			t.Errorf("budget %d: %d attempts", budget, n)
		}
		if _, err := cl.Models(context.Background()); err == nil || len(f.requests()) != 2*(budget+1) {
			t.Errorf("budget %d: models: %v after %d requests", budget, err, len(f.requests()))
		}
	}
}
