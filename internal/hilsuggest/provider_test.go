package hilsuggest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

// providerClient is the HIL surface's client when its provider is a fake
// backend (registered as a kind unique to this package: not parallel).
func providerClient(t *testing.T, fb *jevtest.FakeBackend) *jev.Client {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(jevconf.RegisterKind("hilsuggest-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	ctx := context.Background()
	p := jevconf.Provider{Name: "local", Kind: "hilsuggest-fake", BaseURL: "http://localhost", Model: "llama3.2:3b"}
	if err := jevconf.SaveProvider(ctx, db.DB, p); err != nil {
		t.Fatal(err)
	}
	if err := jevconf.SetSurfaceProvider(ctx, db.DB, "p", jevconf.HIL, "local"); err != nil {
		t.Fatal(err)
	}
	c, err := jevconf.NewClient(ctx, db.DB, "p", "", "", jevconf.HIL)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// With a provider set for the HIL surface, the suggestion comes from it and
// names it in Model; a provider with no probability gates as 0.
func TestSuggestFromAProvider(t *testing.T) {
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{QDecision: "approve", QRisk: "0"}))
	c := providerClient(t, fb)
	s, err := Suggest(context.Background(), c, Input{Editable: map[string]any{"body": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if s.Choice != "approve" || s.P < 0.9 || s.Model != "local/llama3.2:3b" || len(fb.Requests()) != 1 {
		t.Fatalf("suggestion = %+v requests=%d", s, len(fb.Requests()))
	}

	fb.Caps.Probability = jev.ProbNone
	s, err = Suggest(context.Background(), c, Input{Editable: map[string]any{"body": "hi"}})
	if err != nil || s.Choice != "approve" || s.P != 0 || s.RiskP != 0 {
		t.Fatalf("no-probability suggestion = %+v %v", s, err)
	}
}
