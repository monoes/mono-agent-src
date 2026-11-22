package openaicompat

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
)

// The backend itself names the model that answered (jev.Client's fallback to
// its own model is not what this checks): the model sent, or, when neither
// the call nor the record names one, the model the server's response named.
func TestTheResponseNamesTheModelUsed(t *testing.T) {
	f := newFake(t, func(int, seen) (int, any) {
		r := logprobReply(alt{"A", 0.9}, alt{"B", 0.1})
		r["model"] = "served-model"
		return 200, r
	})
	q := map[string]jev.Question{"q": choiceOf("x", "y")}
	for _, c := range []struct{ record, call, want string }{
		{"", "", "served-model"},
		{"rec", "", "rec"},
		{"rec", "call", "call"},
	} {
		b, err := New(jevconf.Provider{BaseURL: f.URL, Model: c.record}, "")
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.Answer(context.Background(), &jev.Request{Model: c.call, Questions: q})
		if err != nil || resp.Model != c.want {
			t.Errorf("record %q call %q: model %q, want %q (%v)", c.record, c.call, resp.Model, c.want, err)
		}
	}
}
