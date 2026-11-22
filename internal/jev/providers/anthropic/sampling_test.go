package anthropic

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// The reviewers' reproduction: 3 of 5 samples say "x", 2 are no allowed
// answer. Dividing by the valid samples once made "x" 1.0; it is
// (3+1)/(5+2) now, below a 0.9 threshold.
func TestJunkSamplesCountTowardTheSamplesAsked(t *testing.T) {
	f := newFake(t, sequence("x", "x", "", "x", "zzz"))
	c := providerClient(t, f.URL, testKey, "")
	resp, err := c.Ask(context.Background(), map[string]any{}, map[string]jev.Question{"q": choice("x", "y")})
	if err != nil {
		t.Fatal(err)
	}
	id, p := jev.GateValue(resp, resp.Answers["q"])
	if id != "x" || !near(p, 4.0/7) || p >= 0.9 {
		t.Fatalf("gate = %s %v", id, p)
	}
}

// The backend itself names the model it used (not jev.Client's fallback):
// the call's, else the record's, else DefaultModel.
func TestTheBackendNamesTheModelUsed(t *testing.T) {
	f := newFake(t, func(int, messagesRequest) (int, string) { return toolReply("yes") })
	for _, c := range []struct{ record, call, want string }{
		{"", "", DefaultModel},
		{"rec", "", "rec"},
		{"rec", "call", "call"},
	} {
		b, err := New(jevconf.Provider{BaseURL: f.URL, Model: c.record}, testKey)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.Answer(context.Background(), &jev.Request{Model: c.call, Questions: oneNoul})
		if err != nil || resp.Model != c.want {
			t.Errorf("record %q call %q: model %q, want %q (%v)", c.record, c.call, resp.Model, c.want, err)
		}
	}
}

// After the first sample, the other four are in flight at once: the fake
// answers none of them until all four arrived.
func TestTheSamplesOfAQuestionRunConcurrently(t *testing.T) {
	var mu sync.Mutex
	n := 0
	all := make(chan struct{})
	f := newFake(t, func(int, messagesRequest) (int, string) {
		mu.Lock()
		n++
		first := n == 1
		if n == Samples {
			close(all)
		}
		mu.Unlock()
		if first {
			return toolReply("yes")
		}
		select {
		case <-all:
			return toolReply("yes")
		case <-time.After(5 * time.Second):
			return http.StatusBadRequest, `{"error":{"type":"sequential"}}`
		}
	})
	c := providerClient(t, f.URL, testKey, "")
	if _, err := c.Ask(context.Background(), map[string]any{}, oneNoul); err != nil {
		t.Fatal(err)
	}
}
