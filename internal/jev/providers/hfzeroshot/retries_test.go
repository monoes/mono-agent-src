package hfzeroshot

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// The client's retry budget reaches the backend: 0 is one attempt and 1 one
// retry, a loading 503 included; at the default a loading model still gets
// its four attempts.
func TestTheClientsRetryBudgetIsHonoured(t *testing.T) {
	for _, c := range []struct {
		budget, status, want int
	}{
		{0, http.StatusServiceUnavailable, 1},
		{1, http.StatusServiceUnavailable, 2},
		{2, http.StatusServiceUnavailable, 4},
		{0, http.StatusBadGateway, 1},
		{1, http.StatusBadGateway, 2},
		{2, http.StatusBadGateway, 3},
	} {
		f := newFake(t, func(classifyRequest) (int, string) { return c.status, `{"error":"x","estimated_time":1}` })
		cl := providerClient(t, f.URL, testKey)
		cl.Backend.(*Backend).sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
		if c.budget != 2 {
			cl.Retries = c.budget
		}
		if _, err := cl.Ask(context.Background(), map[string]any{}, oneNoul); err == nil {
			t.Fatalf("%+v: succeeded", c)
		}
		if n := len(f.requests()); n != c.want {
			t.Errorf("budget %d, status %d: %d attempts, want %d", c.budget, c.status, n, c.want)
		}
	}
}
