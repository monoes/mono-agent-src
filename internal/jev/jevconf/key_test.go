package jevconf

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/secrets"
)

// seedKey stores a key under name in a vault scope and stamps its updated_at.
func seedKey(t *testing.T, db *sql.DB, scope, name, key, updatedAt string) string {
	t.Helper()
	id, err := secrets.Add(context.Background(), db, scope, "secret", name, map[string]string{"secret": key}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE vault_secrets SET updated_at = ? WHERE id = ?`, updatedAt, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func resolveCase(t *testing.T, db *sql.DB, profile, explicit string) (string, string, error) {
	t.Helper()
	return ResolveKey(context.Background(), db, profile, explicit)
}

// No explicit ref: machine vault, then the profile's vault, then env.
func TestResolveKeyOrderAndKeySourceAgree(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	t.Setenv("TYPESAFE_API_KEY", "env-key")

	check := func(wantKey, wantSrc string) {
		t.Helper()
		k, src, err := resolveCase(t, db, "alice", "")
		if err != nil || k != wantKey || src != wantSrc {
			t.Fatalf("ResolveKey = %q %q %v, want %q %q", k, src, err, wantKey, wantSrc)
		}
		if ks, err := KeySource(ctx, db, "alice"); err != nil || ks != wantSrc {
			t.Fatalf("KeySource = %q %v, want %q", ks, err, wantSrc)
		}
	}
	check("env-key", SourceEnv)
	seedKey(t, db, "alice", "Jev Api key", "profile-key", "2026-01-01T00:00:00Z")
	check("profile-key", SourceProfileVault)
	// Another profile does not see alice's key.
	if k, src, _ := resolveCase(t, db, "bob", ""); k != "env-key" || src != SourceEnv {
		t.Fatalf("bob = %q %q", k, src)
	}
	seedKey(t, db, VaultScope, SecretName, "machine-key", "2026-01-01T00:00:00Z")
	check("machine-key", SourceVault)
	// With no profile only the machine vault and env count.
	if k, src, _ := resolveCase(t, db, "", ""); k != "machine-key" || src != SourceVault {
		t.Fatalf("no profile = %q %q", k, src)
	}
}

// An explicit @secret: ref: the profile's vault first, then the machine vault.
func TestResolveKeyExplicitRefPrecedence(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	seedKey(t, db, "alice", "shared", "alice-shared", "2026-01-01T00:00:00Z")
	seedKey(t, db, VaultScope, "shared", "machine-shared", "2026-01-01T00:00:00Z")
	seedKey(t, db, VaultScope, "only-machine", "machine-only", "2026-01-01T00:00:00Z")
	seedKey(t, db, "alice", "only-alice", "alice-only", "2026-01-01T00:00:00Z")

	for _, c := range []struct{ profile, ref, key, src string }{
		{"alice", "@secret:shared", "alice-shared", SourceProfileVault},
		{"bob", "@secret:shared", "machine-shared", SourceVault},
		{"alice", "@secret:only-machine", "machine-only", SourceVault},
		{"alice", "@secret:only-alice", "alice-only", SourceProfileVault},
	} {
		if k, src, err := resolveCase(t, db, c.profile, c.ref); err != nil || k != c.key || src != c.src {
			t.Errorf("%s %s = %q %q %v, want %q %q", c.profile, c.ref, k, src, err, c.key, c.src)
		}
	}
	if _, _, err := resolveCase(t, db, "bob", "@secret:only-alice"); !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("bob read alice's entry: %v", err)
	}
	if c, err := NewClient(context.Background(), db, "alice", "@secret:only-alice", "", Capture); err != nil || c == nil {
		t.Fatalf("NewClient with a profile ref: %v", err)
	}
}

// A node's explicit reference is read as secrets.Resolve reads one: in a run
// the user hasn't approved it is refused, spaces around it or not, and no
// entry is read (#845 review: ai.choose trims its key before reading it).
func TestResolveKeyRefusesAReferenceTheRunMayNotRead(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	seedKey(t, db, "alice", "github", "ghp-alice", "2026-01-01T00:00:00Z")
	ctx := secrets.RefuseResolving(context.Background(), "the chat wrote this workflow")
	for _, ref := range []string{"@secret:github", " @secret:github "} {
		k, _, err := ResolveKey(ctx, db, "alice", ref)
		if !errors.Is(err, secrets.ErrResolveRefused) || k != "" || !strings.Contains(err.Error(), "the chat wrote this workflow") {
			t.Errorf("%q in an unapproved run = %q, %v; want the refusal", ref, k, err)
		}
	}
	if k, _, err := ResolveKey(context.Background(), db, "alice", " @secret:github "); err != nil || k != "ghp-alice" {
		t.Errorf("the user's run = %q, %v", k, err)
	}
}

// Absent is jev.ErrNoAPIKey; present but undecryptable is ErrKeyUnreadable.
func TestResolveKeyAbsentVersusUnreadable(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	if _, _, err := resolveCase(t, db, "alice", ""); !errors.Is(err, jev.ErrNoAPIKey) || errors.Is(err, ErrKeyUnreadable) {
		t.Fatalf("absent: %v", err)
	}
	id := seedKey(t, db, VaultScope, SecretName, "k", "2026-01-01T00:00:00Z")
	if _, err := db.Exec(`UPDATE vault_secrets SET ciphertext = x'0102030405060708090a0b0c0d0e0f10' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	_, _, err := resolveCase(t, db, "alice", "")
	if !errors.Is(err, ErrKeyUnreadable) || errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("unreadable: %v", err)
	}
	// Env still rescues an unreadable stored key.
	t.Setenv("TYPESAFE_API_KEY", "env-key")
	if k, src, err := resolveCase(t, db, "alice", ""); err != nil || k != "env-key" || src != SourceEnv {
		t.Fatalf("env rescue = %q %q %v", k, src, err)
	}
}

// Resolving never writes: no vault entry or setting appears.
func TestResolveKeyDoesNotWrite(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	seedKey(t, db, "alice", SecretName, "profile-key", "2026-01-01T00:00:00Z")
	var before, after int
	db.QueryRow(`SELECT (SELECT COUNT(*) FROM vault_secrets) + (SELECT COUNT(*) FROM settings)`).Scan(&before)
	for i := 0; i < 2; i++ {
		_, _, _ = resolveCase(t, db, "alice", "")
		_, _ = KeySource(context.Background(), db, "alice")
	}
	db.QueryRow(`SELECT (SELECT COUNT(*) FROM vault_secrets) + (SELECT COUNT(*) FROM settings)`).Scan(&after)
	if before != after {
		t.Fatalf("rows %d -> %d: a read path wrote", before, after)
	}
}

func TestAdopt(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	ctx := context.Background()
	if _, err := Adopt(ctx, db, ""); !errors.Is(err, jev.ErrNoAPIKey) {
		t.Fatalf("nothing to adopt: %v", err)
	}
	seedKey(t, db, "alice", "typesafe", "old-key", "2026-01-01T00:00:00Z")
	seedKey(t, db, "bob", "Jev Api key", "new-key", "2026-06-01T00:00:00Z")
	seedKey(t, db, "carol", "typesafe", "mid-key", "2026-03-01T00:00:00Z")

	res, err := Adopt(ctx, db, "")
	if err != nil || res.FromProfile != "bob" || res.Candidates != 3 {
		t.Fatalf("Adopt = %+v %v", res, err)
	}
	if k, src, err := resolveCase(t, db, "", ""); err != nil || k != "new-key" || src != SourceVault {
		t.Fatalf("after adopt = %q %q %v", k, src, err)
	}
	for _, p := range []string{"alice", "bob", "carol"} {
		if es, _ := secrets.List(ctx, db, p); len(es) != 1 {
			t.Errorf("profile %s vault changed: %+v", p, es)
		}
	}
	if _, err := Adopt(ctx, db, "alice"); !errors.Is(err, ErrMachineKeyExists) {
		t.Fatalf("second adopt: %v", err)
	}
	if _, err := Adopt(ctx, db, VaultScope); err == nil {
		t.Fatal("the machine scope is not a profile")
	}
}

// An empty stored key is reported as empty, not as an unreadable nil error.
func TestAdoptEmptyKey(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	seedKey(t, db, "alice", "typesafe", " ", "2026-01-01T00:00:00Z")
	_, err := Adopt(context.Background(), db, "alice")
	if !errors.Is(err, ErrKeyUnreadable) || !strings.Contains(err.Error(), "is empty") || strings.Contains(err.Error(), "<nil>") {
		t.Fatalf("Adopt = %v", err)
	}
}

func TestAdoptFromNamedProfile(t *testing.T) {
	keyring.MockInit()
	db := newDB(t)
	ctx := context.Background()
	seedKey(t, db, "alice", "typesafe", "old-key", "2026-01-01T00:00:00Z")
	seedKey(t, db, "bob", "typesafe", "new-key", "2026-06-01T00:00:00Z")
	if res, err := Adopt(ctx, db, "alice"); err != nil || res.FromProfile != "alice" {
		t.Fatalf("Adopt = %+v %v", res, err)
	}
	if k, _, _ := resolveCase(t, db, "", ""); k != "old-key" {
		t.Fatalf("key = %q, want alice's", k)
	}
}

// Every profile resolves the same machine key.
func TestResolveKeyIsMachineWide(t *testing.T) {
	keyring.MockInit()
	t.Setenv("TYPESAFE_API_KEY", "")
	db := newDB(t)
	seedKey(t, db, VaultScope, SecretName, "machine-key", "2026-01-01T00:00:00Z")
	for _, profile := range []string{"a", "b"} {
		c, err := NewClient(context.Background(), db, profile, "", "", Capture)
		if err != nil || c == nil {
			t.Fatalf("profile %s: %v", profile, err)
		}
	}
}
