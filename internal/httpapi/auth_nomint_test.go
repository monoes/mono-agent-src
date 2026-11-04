package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/monoes/mono-agent/internal/secrets"
)

func vaultRowCounts(t *testing.T, s *Server) (secretRows, keyRows int) {
	t.Helper()
	db := s.rt.db.DB
	if err := db.QueryRow(`SELECT COUNT(*) FROM vault_secrets`).Scan(&secretRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vault_keys`).Scan(&keyRows); err != nil {
		t.Fatal(err)
	}
	return
}

// Authentication has no side effect: a request carrying a wrong bearer for a
// profile that has no token yet must not create the token (a vault entry, a
// key, any row).
func TestProfileAuthWrongBearerMintsNothing(t *testing.T) {
	s := newTestServer(t, false)
	s0, k0 := vaultRowCounts(t, s)

	for _, path := range []string{"/profiles/default/workflows", "/profiles/default/workflows"} {
		rec := doReq(t, s, http.MethodGet, path, "not-the-token", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s with a wrong bearer: status %d, want 401", path, rec.Code)
		}
	}

	if s1, k1 := vaultRowCounts(t, s); s1 != s0 || k1 != k0 {
		t.Fatalf("an unauthenticated request wrote to the vault: vault_secrets %d -> %d, vault_keys %d -> %d", s0, s1, k0, k1)
	}
	entries, err := secrets.List(context.Background(), s.rt.db.DB, "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == tokenSecretName {
			t.Fatal("a wrong bearer made the server mint the profile's token")
		}
	}
}

// The first-use mint is the server's start (ensureTokens, called by Serve):
// afterwards the profile has its token, and that token authenticates.
func TestEnsureTokensAtStartMintsAndTokenWorks(t *testing.T) {
	s := newTestServer(t, false)
	if tok, err := existingToken(context.Background(), s.rt.db.DB, "default"); err != nil || tok != "" {
		t.Fatalf("fresh machine already has a token: %q, %v", tok, err)
	}
	s.ensureTokens(context.Background())
	tok, err := existingToken(context.Background(), s.rt.db.DB, "default")
	if err != nil || tok == "" {
		t.Fatalf("server start did not mint the token: %q, %v", tok, err)
	}
	if rec := doReq(t, s, http.MethodGet, "/profiles/default/workflows", tok, nil); rec.Code != http.StatusOK {
		t.Fatalf("the minted token was refused: %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, s, http.MethodGet, "/profiles/default/workflows", tok+"x", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong token: %d, want 401", rec.Code)
	}
}
