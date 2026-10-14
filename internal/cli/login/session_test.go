package login

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/storage"
)

// sessionsTestEnv is a migrated DB holding a "work" profile beside "default".
type sessionsTestEnv struct {
	dbPath string
	db     *storage.Database
}

func newSessionsTestEnv(t *testing.T) *sessionsTestEnv {
	t.Helper()
	db, dbPath := newTestDB(t)
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name, created_at) VALUES ('default', 'Default', '2026-01-01'), ('work', 'Work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	return &sessionsTestEnv{dbPath: dbPath, db: db}
}

// run executes `[--profile P] [--json] login|logout <args>` with stdin and returns stdout,
// stderr and the exit code main would use.
func (e *sessionsTestEnv) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	profile, jsonOut := "", false
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		switch args[0] {
		case "--profile":
			profile, args = args[1], args[2:]
		case "--json":
			jsonOut, args = true, args[1:]
		default:
			t.Fatalf("run: unexpected flag %s", args[0])
		}
	}
	d := testDeps(e.dbPath, profile, jsonOut)
	cmd := NewCommand(d)
	if args[0] == "logout" {
		cmd = NewLogoutCommand(d)
	}
	return runCmd(cmd, stdin, args[1:]...)
}

// seedSession adds a crawler_sessions row whose cookies live in the vault.
func (e *sessionsTestEnv) seedSession(t *testing.T, profile, platform, user string, ttl time.Duration) int {
	t.Helper()
	ctx := context.Background()
	if err := upsertSessionRowTTL(ctx, e.db.DB, profile, platform, user, []byte(`[{"name":"sid","value":"cookie-secret-value"}]`), ttl); err != nil {
		t.Fatal(err)
	}
	var id int
	if err := e.db.DB.QueryRow(`SELECT id FROM crawler_sessions WHERE profile_id = ? AND platform = ? AND username = ?`, profile, platform, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLoginTestSession(t *testing.T) {
	t.Parallel()
	e := newSessionsTestEnv(t)
	good := e.seedSession(t, "work", "linkedin", "me", 24*time.Hour)
	expired := e.seedSession(t, "work", "x", "me", -time.Hour)
	other := e.seedSession(t, "default", "instagram", "me", 24*time.Hour)
	noCookies := e.seedSession(t, "work", "tiktok", "me", 24*time.Hour)
	if _, err := e.db.DB.Exec(`UPDATE crawler_sessions SET vault_ref = '' WHERE id = ?`, noCookies); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := e.run(t, "", "--profile", "work", "--json", "login", "test", strconv.Itoa(good))
	if code != 0 {
		t.Fatalf("good session: code %d, stderr %s", code, errOut)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil || got["status"] != "ok" || got["platform"] != "linkedin" {
		t.Fatalf("stdout = %q (%v)", out, err)
	}
	if strings.Contains(out, "cookie-secret-value") {
		t.Fatal("cookies printed")
	}

	for _, tc := range []struct {
		id   int
		code int
		msg  string
	}{
		{expired, 4, "session expired"},
		{noCookies, 4, "no cookies stored"},
		{other, 2, "session not found"}, // another profile's session
		{99999, 2, "session not found"},
	} {
		_, errOut, code := e.run(t, "", "--profile", "work", "--json", "login", "test", strconv.Itoa(tc.id))
		if code != tc.code || !strings.Contains(errOut, tc.msg) {
			t.Errorf("login test %d: code %d, stderr %q; want %d %q", tc.id, code, errOut, tc.code, tc.msg)
		}
	}
	if _, _, code := e.run(t, "", "--json", "login", "test", "abc"); code != 3 {
		t.Errorf("non-numeric id: code %d, want 3", code)
	}
}

func TestLoginDeleteSessionRemovesVaultEntry(t *testing.T) {
	t.Parallel()
	e := newSessionsTestEnv(t)
	id := e.seedSession(t, "work", "linkedin", "me", 24*time.Hour)
	other := e.seedSession(t, "default", "linkedin", "me", 24*time.Hour)
	var ref string
	_ = e.db.DB.QueryRow(`SELECT vault_ref FROM crawler_sessions WHERE id = ?`, id).Scan(&ref)

	// Another profile's id is not found, and stays.
	if _, errOut, code := e.run(t, "", "--profile", "work", "--json", "login", "delete", strconv.Itoa(other)); code != 2 || !strings.Contains(errOut, "not found") {
		t.Fatalf("cross-profile delete: code %d, %q", code, errOut)
	}
	out, errOut, code := e.run(t, "", "--profile", "work", "--json", "login", "delete", strconv.Itoa(id))
	if code != 0 || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("delete: code %d, out %q, stderr %q", code, out, errOut)
	}
	var n int
	_ = e.db.DB.QueryRow(`SELECT COUNT(*) FROM crawler_sessions WHERE id IN (?, ?)`, id, other).Scan(&n)
	if n != 1 {
		t.Fatalf("%d session rows left, want only the other profile's", n)
	}
	_ = e.db.DB.QueryRow(`SELECT COUNT(*) FROM vault_secrets WHERE id = ?`, ref).Scan(&n)
	if n != 0 {
		t.Fatal("the session's vault entry was left behind")
	}
}

// logout <platform> takes the cookies' vault entry with the session.
func TestLogoutRemovesVaultEntry(t *testing.T) {
	t.Parallel()
	e := newSessionsTestEnv(t)
	id := e.seedSession(t, "work", "linkedin", "me", 24*time.Hour)
	var ref string
	_ = e.db.DB.QueryRow(`SELECT vault_ref FROM crawler_sessions WHERE id = ?`, id).Scan(&ref)
	if _, errOut, code := e.run(t, "", "--profile", "work", "logout", "linkedin"); code != 0 {
		t.Fatalf("logout: %d %s", code, errOut)
	}
	var n int
	_ = e.db.DB.QueryRow(`SELECT COUNT(*) FROM vault_secrets WHERE id = ?`, ref).Scan(&n)
	if n != 0 {
		t.Fatal("logout left the session's vault entry behind")
	}
}
