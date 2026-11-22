package dynorg

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
)

// A provider without probabilities gives no confidence to act on: its pick
// is no Chooser answer, so every staffing question falls back to the rule.
// With probabilities the pick and its p are as before.
func TestJevChooserTreatsANoProbabilityPickAsNoAnswer(t *testing.T) {
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{"choice": "b"}))
	c := JevChooser{Client: jev.NewBackendClient("local", "m", fb)}
	opts := map[string]string{"a": "first", "b": "second"}

	id, p, err := c.Choose(context.Background(), map[string]any{}, "Which?", opts)
	if err != nil || id != "b" || p <= 0.5 {
		t.Fatalf("with probabilities: %q %v %v", id, p, err)
	}
	fb.Caps.Probability = jev.ProbNone
	if id, p, err := c.Choose(context.Background(), map[string]any{}, "Which?", opts); err == nil || id != "" || p != 0 {
		t.Fatalf("without probabilities: %q %v %v, want an error the callers fall back on", id, p, err)
	}
	if fb.Calls() != 2 {
		t.Errorf("%d calls", fb.Calls())
	}
}
