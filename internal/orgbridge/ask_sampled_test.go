package orgbridge

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// Linking a reply resumes a run: a unanimous sample below the 0.9 default
// links nothing.
func TestASampledAskMatchKeepsTheConfiguredThreshold(t *testing.T) {
	db := newTestDB(t)
	enableAsks(t, db, "p")
	target := createAsk(t, db, Ask{ProfileID: "p", OrgName: "growth", EndpointRoleID: "bot", ExecutionID: "e", NodeID: "n", Question: "Which region?"})
	fb := jevtest.NewFakeBackend(nil)
	fb.SetSampled(jevtest.Fixed(map[string]string{"ask": target.ID}), 5)
	asked := 0
	m := &AskMatcher{DB: db, NewClient: func(context.Context, string) (*jev.Client, error) {
		asked++
		return &jev.Client{Provider: "local", Backend: fb, Model: "m"}, nil
	}}
	if got, ok := m.ask(context.Background(), "p", "growth", "bot", "re", "north"); ok || asked != 1 || len(fb.Requests()) != 1 {
		t.Fatalf("%+v %v (clients %d)", got, ok, asked)
	}
}
