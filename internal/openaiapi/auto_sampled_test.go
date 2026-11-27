package openaiapi

import (
	"context"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/testdb"
)

// A threshold a sampled pick cannot read is read as unanimous: Choose's
// ceiling is what a unanimous pick reads (0 for any other provider).
func TestPickAutoReadsAThresholdAgainstTheSampledCeiling(t *testing.T) {
	cands := []ModelInfo{cand("claude/default", ChatOnly, true, 0.001, true, 900), cand("codex/gpt-6-astra", Sandboxed, true, 0.01, true, 4000)}
	for _, c := range []struct {
		name            string
		p, ceiling, thr float64
		by              string
	}{
		{"unanimous", 6.0 / 7, 6.0 / 7, 0.9, "jev"},
		{"4 of 5", 5.0 / 7, 6.0 / 7, 0.9, "rule"},
		{"4 of 5 at a threshold it reads", 5.0 / 7, 6.0 / 7, 0.7, "jev"},
		{"not sampled", 6.0 / 7, 0, 0.9, "rule"},
	} {
		f := &fakeAuto{id: "codex/gpt-6-astra", p: c.p, ceiling: c.ceiling, thr: c.thr}
		h := autoGateway(t, f)
		if pick := h.g.pickAuto(context.Background(), "alice", "hi", cands); pick.By != c.by {
			t.Errorf("%s: picked by %s, want %s", c.name, pick.By, c.by)
		}
	}
}

// The production Choose reports the ceiling of a sampled provider's pick.
func TestDefaultAutoReportsTheSampledCeiling(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	fb := jevtest.NewFakeBackend(nil)
	t.Cleanup(jevconf.RegisterKind("fake-auto-sampled", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(ctx, db.DB, jevconf.Provider{Name: "local", Kind: "fake-auto-sampled", BaseURL: "http://localhost:11434/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "alice", jevconf.APIAuto, "local"); err != nil {
		t.Fatal(err)
	}
	fb.SetSampled(jevtest.Fixed(map[string]string{autoQuestionID: "b"}), 5)
	id, p, ceiling, err := DefaultAuto(db.DB).Choose(ctx, "alice", "hi", map[string]string{"a": "A", "b": "B"})
	if err != nil || id != "b" || ceiling != jev.UnanimousProbability(5, 2) || p < jevconf.CapThreshold(0.9, ceiling) {
		t.Fatalf("Choose = %q, %v, %v, %v", id, p, ceiling, err)
	}
}
