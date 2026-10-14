package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/librarycli"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/library/libraryfake"
	"github.com/monoes/mono-agent/internal/storage"
)

// Only the host the build signs in to receives the session; MONOES_BASE_URL elsewhere
// refuses a library login up front. (The short timeout only bounds the run before the
// aliases exist, when this command waits for a browser that never comes.)
func TestLibraryLoginNeedsTheAccountHost(t *testing.T) {
	f := newLibFixture(t)
	t.Setenv("MONOES_BASE_URL", "http://127.0.0.1:9")
	out, err := f.run("library", "login", "--no-browser", "--timeout", "2s")
	if exitCodeFor(err) != 3 || !strings.Contains(out, "-tags devaccount") {
		t.Fatalf("library login at another host: %v %s", err, out)
	}
}

// A login a release before the machine session left in a profile's vault keeps
// serving that profile's reads until a session replaces it, and `library logout`
// forgets it (spec D21, D22: a dormant release changes nothing a user sees).
func TestAnOlderLibraryLoginKeepsWorkingAndLogoutForgetsIt(t *testing.T) {
	noSeed(t)
	f := newLibFixture(t)
	f.fake.Add("monoes", "automation", "hackernews", "Hacker News", "official", "1.1.0", pack(t, "hackernews", ""),
		map[string]any{"automation_id": "hackernews"})
	f.must(nil, "profile", "list") // makes the database
	db, err := initDB(&globalConfig{DBPath: filepath.Join(f.home, ".monoagent", "monoagent.db")})
	if err != nil {
		t.Fatal(err)
	}
	seedOlderLogin(t, db, f.fake)
	db.Close()

	var st librarycli.Status
	f.must(&st, "library", "status")
	if !st.LoggedIn || st.Method != "pkce" {
		t.Fatalf("the older login is not seen: %+v", st)
	}
	var list libList
	f.must(&list, "library", "list", "--scope", "official")
	if list.Total != 1 {
		t.Fatalf("the older login does not serve reads: %+v", list)
	}
	f.must(nil, "library", "logout")
	f.must(&st, "library", "status")
	if st.LoggedIn || f.fake.Requests["POST /api/auth/oauth2/revoke"] == 0 {
		t.Fatalf("after logout: %+v (revocations %d)", st, f.fake.Requests["POST /api/auth/oauth2/revoke"])
	}
}

// seedOlderLogin stores in the default profile's vault the login a release before
// the machine session would have: an opaque token and a refresh token.
func seedOlderLogin(t *testing.T, db *storage.Database, fake *libraryfake.Server) {
	t.Helper()
	access, refresh := fake.NewGrant("ada")
	vs := &library.VaultStore{DB: db.DB, ProfileID: "default", BaseURL: fake.URL}
	tok := &library.Token{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", Method: "pkce", BaseURL: fake.URL,
		User: &library.User{ID: "u-ada", Username: "ada", Email: "ada@example.com"}}
	if err := vs.Save(t.Context(), tok); err != nil {
		t.Fatal(err)
	}
}
