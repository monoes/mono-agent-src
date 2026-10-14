package login

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/secrets"
)

func TestUpsertSessionRow_CreatesThenUpdatesInPlace(t *testing.T) {
	t.Parallel()
	db, _ := newTestDB(t)
	ctx := context.Background()

	firstCookies := []byte(`[{"name":"sid","value":"abc"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", firstCookies); err != nil {
		t.Fatalf("first upsertSessionRow: %v", err)
	}

	var vaultRef string
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef); err != nil {
		t.Fatalf("reading session row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one session row, got %d", count)
	}
	if vaultRef == "" {
		t.Fatal("expected vault_ref to be populated")
	}

	resolved, _, err := secrets.DecryptFields(ctx, db.DB, "default", vaultRef)
	if err != nil {
		t.Fatalf("DecryptFields: %v", err)
	}
	if resolved["cookies"] != string(firstCookies) {
		t.Fatalf("unexpected cookies field: %q", resolved["cookies"])
	}

	// Second call for the same platform+username must update in place, not
	// create a second row or a second vault entry.
	secondCookies := []byte(`[{"name":"sid","value":"xyz"}]`)
	if err := upsertSessionRow(ctx, db.DB, "default", "instagram", "alice", secondCookies); err != nil {
		t.Fatalf("second upsertSessionRow: %v", err)
	}
	var vaultRef2 string
	if err := db.DB.QueryRow(`SELECT COUNT(*), vault_ref FROM crawler_sessions WHERE platform = 'instagram' AND username = 'alice'`).Scan(&count, &vaultRef2); err != nil {
		t.Fatalf("reading session row after update: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected still exactly one session row, got %d", count)
	}
	if vaultRef2 != vaultRef {
		t.Fatalf("expected the same vault entry to be reused, got %q want %q", vaultRef2, vaultRef)
	}
	resolved, _, err = secrets.DecryptFields(ctx, db.DB, "default", vaultRef2)
	if err != nil {
		t.Fatalf("DecryptFields after update: %v", err)
	}
	if resolved["cookies"] != string(secondCookies) {
		t.Fatalf("expected updated cookies, got %q", resolved["cookies"])
	}
}

// TestLoginStatusTableRightAlignsNumericIDColumn guards against a
// tablewriter v1 migration regression: v0.0.5 auto-right-aligned any cell
// whose content was purely numeric (via regex), with no explicit
// SetColumnAlignment call needed; v1's shared newPlainTable helper has no
// such content-sniffing default, so a numeric ID column that never
// explicitly requested alignment silently went from right- to
// left-aligned. Seeds two sessions with deliberately different ID widths
// (1 vs 100, inserted directly so the numeric IDs are controlled rather
// than relying on autoincrement) and asserts the shorter ID is
// left-padded to align with the wider one, not left-flush against it.
func TestLoginStatusTableRightAlignsNumericIDColumn(t *testing.T) {
	t.Parallel()
	db, dbPath := newTestDB(t)
	future := time.Now().Add(24 * time.Hour)

	insert := func(id int, username string) {
		t.Helper()
		if _, err := db.DB.Exec(
			`INSERT INTO crawler_sessions (id, username, platform, cookies_json, expiry, when_added, profile_id, vault_ref) VALUES (?, ?, ?, '{}', ?, ?, 'default', '')`,
			id, username, "instagram", future, time.Now(),
		); err != nil {
			t.Fatalf("seeding session id=%d: %v", id, err)
		}
	}
	insert(1, "shortid")
	insert(100, "longid")

	cmd := NewCommand(testDeps(dbPath, "default", false))
	cmd.SetArgs([]string{"status"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("login status: %v", err)
	}

	lines := strings.Split(out.String(), "\n")
	var line1, line100 string
	for _, l := range lines {
		if strings.Contains(l, "shortid") {
			line1 = l
		}
		if strings.Contains(l, "longid") {
			line100 = l
		}
	}
	if line1 == "" || line100 == "" {
		t.Fatalf("expected both session rows in output, got:\n%s", out.String())
	}
	pos1 := strings.Index(line1, "1")
	pos100 := strings.Index(line100, "100")
	if pos1 <= pos100 {
		t.Fatalf("expected the single-digit ID to start further right than the 3-digit ID (right-alignment), got ID \"1\" at column %d and \"100\" at column %d\nrow1:   %q\nrow100: %q", pos1, pos100, line1, line100)
	}
}

// `login status --json` uses snake_case keys and prints [] (not null) when
// there is nothing to report.
func TestLoginStatusJSONShape(t *testing.T) {
	t.Parallel()
	db, dbPath := newTestDB(t) // and no installed automations with a login block
	run := func() string {
		cmd := NewCommand(testDeps(dbPath, "default", true))
		cmd.SetArgs([]string{"status"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	if got := strings.TrimSpace(run()); got != "[]" && !strings.Contains(got, `"status": "logged_out"`) {
		t.Fatalf("empty output = %q, want []", got)
	}
	exp := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if _, err := db.DB.Exec(`INSERT INTO crawler_sessions (id, username, platform, cookies_json, expiry, when_added, profile_id, vault_ref)
		VALUES (7, 'me', 'linkedin', '{}', ?, ?, 'default', '')`, exp, exp.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(run()), &rows); err != nil {
		t.Fatal(err)
	}
	var li map[string]any
	for _, r := range rows {
		if r["platform"] == "linkedin" {
			li = r
		}
	}
	if li == nil || li["id"] != float64(7) || li["username"] != "me" || li["expiry"] != "2026-10-01T12:00:00Z" || li["when_added"] != "2026-10-01T11:00:00Z" {
		t.Fatalf("row = %v (all rows: %v)", li, rows)
	}
	if _, old := li["Platform"]; old {
		t.Fatal("Go field names leaked into the JSON")
	}
}
