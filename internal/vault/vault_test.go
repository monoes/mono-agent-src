package vault

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDBFromContext_Absent(t *testing.T) {
	if db := DBFromContext(context.Background()); db != nil {
		t.Fatalf("DBFromContext on empty context = %v, want nil", db)
	}
}

func TestProfileIDFromContext(t *testing.T) {
	if _, err := ProfileIDFromContext(context.Background()); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("ProfileIDFromContext(empty) err = %v, want ErrNoProfile", err)
	}
	ctx := ContextWithProfileID(context.Background(), "team-a")
	if got, err := ProfileIDFromContext(ctx); err != nil || got != "team-a" {
		t.Fatalf("ProfileIDFromContext = %q, %v, want team-a", got, err)
	}
	ctx = ContextWithProfileID(context.Background(), "")
	if _, err := ProfileIDFromContext(ctx); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("ProfileIDFromContext(empty string) err = %v, want ErrNoProfile", err)
	}
}

func TestExecIDsFromContext(t *testing.T) {
	wf, ex := ExecIDsFromContext(context.Background())
	if wf != "" || ex != "" {
		t.Fatalf("ExecIDsFromContext(empty) = (%q,%q), want empty", wf, ex)
	}
	ctx := ContextWithExecIDs(context.Background(), "wf1", "ex1")
	if wf, ex = ExecIDsFromContext(ctx); wf != "wf1" || ex != "ex1" {
		t.Fatalf("ExecIDsFromContext = (%q,%q), want (wf1,ex1)", wf, ex)
	}
}

func TestResolve_NonRefPassThrough(t *testing.T) {
	// A value without the "@" prefix is returned unchanged, no DB needed.
	got, err := Resolve(context.Background(), nil, "plain-value")
	if err != nil {
		t.Fatalf("Resolve(non-ref) error: %v", err)
	}
	if got != "plain-value" {
		t.Fatalf("Resolve(non-ref) = %q, want unchanged", got)
	}
}

func TestResolveConfig_NilDBIsNoOp(t *testing.T) {
	cfg := map[string]interface{}{"image": "@img-001", "other": 42}
	if err := ResolveConfig(context.Background(), nil, maps.Clone(cfg), cfg); err != nil {
		t.Fatalf("ResolveConfig(nil db) error: %v", err)
	}
	if cfg["image"] != "@img-001" {
		t.Fatalf("ResolveConfig(nil db) mutated config: %v", cfg["image"])
	}
}

func newVaultTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE vault_images (id TEXT PRIMARY KEY, seq INTEGER, path TEXT, filename TEXT, profile_id TEXT DEFAULT 'default')`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE vault_documents (id TEXT PRIMARY KEY, path TEXT, filename TEXT, profile_id TEXT DEFAULT 'default', source TEXT DEFAULT 'upload')`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

func TestResolveConfig_ResolvesExistingRef(t *testing.T) {
	db := newVaultTestDB(t)
	if _, err := db.Exec(`INSERT INTO vault_images (id, path, profile_id) VALUES ('img-001','/tmp/pic.png','default')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	cfg := map[string]interface{}{"image": "@img-001"}
	if err := ResolveConfig(ContextWithProfileID(context.Background(), "default"), db, maps.Clone(cfg), cfg); err != nil {
		t.Fatalf("ResolveConfig error: %v", err)
	}
	if cfg["image"] != "/tmp/pic.png" {
		t.Fatalf("ResolveConfig did not resolve ref: got %v", cfg["image"])
	}
}

func TestResolveConfig_MissingRefIsNonFatal(t *testing.T) {
	db := newVaultTestDB(t)
	cfg := map[string]interface{}{"image": "@img-999"}
	// A missing image must not fail the whole config resolution; the ref is left as-is.
	if err := ResolveConfig(ContextWithProfileID(context.Background(), "default"), db, maps.Clone(cfg), cfg); err != nil {
		t.Fatalf("ResolveConfig(missing) error: %v", err)
	}
	if cfg["image"] != "@img-999" {
		t.Fatalf("ResolveConfig(missing) = %v, want unchanged ref", cfg["image"])
	}
}

func TestResolveConfig_ProfileScoped(t *testing.T) {
	db := newVaultTestDB(t)
	if _, err := db.Exec(`INSERT INTO vault_images (id, path, profile_id) VALUES ('img-005','/tmp/a.png','team-a')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// Default profile must NOT resolve another profile's image.
	cfg := map[string]interface{}{"image": "@img-005"}
	if err := ResolveConfig(ContextWithProfileID(context.Background(), "default"), db, maps.Clone(cfg), cfg); err != nil {
		t.Fatalf("ResolveConfig error: %v", err)
	}
	if cfg["image"] != "@img-005" {
		t.Fatalf("default profile resolved another profile's image: %v", cfg["image"])
	}
	// The owning profile resolves it.
	ctx := ContextWithProfileID(context.Background(), "team-a")
	cfg = map[string]interface{}{"image": "@img-005"}
	if err := ResolveConfig(ctx, db, maps.Clone(cfg), cfg); err != nil {
		t.Fatalf("ResolveConfig(team-a) error: %v", err)
	}
	if cfg["image"] != "/tmp/a.png" {
		t.Fatalf("owning profile did not resolve image: %v", cfg["image"])
	}
}

func TestResolveConfig_NoProfileIsAnError(t *testing.T) {
	db := newVaultTestDB(t)
	cfg := map[string]interface{}{"image": "@img-001"}
	if err := ResolveConfig(context.Background(), db, maps.Clone(cfg), cfg); !errors.Is(err, ErrNoProfile) {
		t.Fatalf("ResolveConfig without a profile err = %v, want ErrNoProfile", err)
	}
	if err := ResolveConfig(context.Background(), db, nil, map[string]interface{}{"x": "plain"}); err != nil {
		t.Fatalf("no refs needs no profile, got %v", err)
	}
}
