package doctor

import (
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/storage"
)

// jev.api checks the default decision provider: its models when it lists
// them, else one tiny question; and the hook names that provider.
func TestHealthEnvChecksTheDefaultDecisionProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "doctor.db")
	db, err := storage.NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fb := jevtest.NewFakeBackend(nil)
	t.Cleanup(jevconf.RegisterKind("doctor-fake", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	ctx := t.Context()
	if err := jevconf.SaveProvider(ctx, db.DB, jevconf.Provider{Name: "local", Kind: "doctor-fake", BaseURL: "http://localhost:9"}); err != nil {
		t.Fatal(err)
	}

	env, closeFn := NewHealthEnv(EnvDeps{DBPath: func() string { return path }, ProfileID: func() string { return "default" },
		Version: func() string { return "test" }}, nil)
	defer closeFn()
	if p := env.JevProvider(); p != "typesafe" {
		t.Fatalf("default provider with none chosen = %q", p)
	}
	if err := jevconf.SetDefaultProvider(ctx, db.DB, "local"); err != nil {
		t.Fatal(err)
	}
	if p := env.JevProvider(); p != "local" {
		t.Fatalf("default provider = %q", p)
	}
	// No model listing: one question is asked.
	if err := env.JevModels(ctx); err != nil || fb.Calls() != 1 {
		t.Fatalf("check without models: %v (calls %d)", err, fb.Calls())
	}
	// A provider that lists models is not asked anything.
	fb.ModelList = []jev.Model{{ID: "m"}}
	if err := env.JevModels(ctx); err != nil || fb.Calls() != 1 {
		t.Fatalf("check with models: %v (calls %d)", err, fb.Calls())
	}
}
