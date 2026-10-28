package profilectx

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func seed(t *testing.T, db *sql.DB, id, name, root string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO profiles (id, name) VALUES (?, ?)`, id, name)
	if root != "" {
		mustExec(t, db, `UPDATE profiles SET root_dir = ? WHERE id = ?`, root, id)
	}
}

const (
	secretValue  = "hunter2-SECRET-VALUE"
	cookieSecret = "COOKIE-SECRET-TOKEN"
)

func fill(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	mustExec(t, db, `INSERT INTO crawler_sessions (username, platform, cookies_json, expiry, profile_id)
		VALUES ('alice_handle', 'instagram', ?, '2099-01-01', ?)`, `{"sessionid":"`+cookieSecret+`"}`, id)
	mustExec(t, db, `INSERT INTO tasks (profile_id, title, status, position, created_at, updated_at)
		VALUES (?, 'write the report', 'ready', 1, '2026-01-01', '2026-01-02'), (?, 'old thing', 'done', 2, '2026-01-01', '2026-01-01')`, id, id)
	mustExec(t, db, `INSERT INTO people (id, platform_username, platform, full_name, profile_id) VALUES ('p1', 'bob', 'linkedin', 'Bob Builder', ?)`, id)
	mustExec(t, db, `INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1', 'Daily digest', 1, ?)`, id)
	mustExec(t, db, `INSERT INTO vault_secrets (id, seq, profile_id, kind, name, username, ciphertext, nonce, created_at, updated_at)
		VALUES ('s1', 1, ?, 'login', 'github-token', 'octo-user', ?, x'00', '2026-01-01', '2026-01-01')`, id, []byte(secretValue))
	mustExec(t, db, `INSERT INTO vault_documents (id, seq, path, filename, profile_id, created_at) VALUES ('doc-001', 1, '/x/brief.pdf', 'brief.pdf', ?, '2026-01-01')`, id)
}

func TestBuildNamesTheRightProfile(t *testing.T) {
	db := testDB(t)
	rootA, rootB := t.TempDir(), t.TempDir()
	seed(t, db, "pa", "Alpha Studio", rootA)
	seed(t, db, "pb", "Beta Lab", rootB)
	fill(t, db, "pa")

	a, err := Build(context.Background(), db, "pa", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Profile.Name != "Alpha Studio" || a.Folder != rootA {
		t.Fatalf("profile a: %+v", a)
	}
	blockA := a.Block()
	for _, want := range []string{"Alpha Studio", "pa", rootA, "instagram", "alice_handle", "write the report", "Bob Builder", "Daily digest", "github-token", "brief.pdf"} {
		if !strings.Contains(blockA, want) {
			t.Errorf("block lacks %q:\n%s", want, blockA)
		}
	}
	if strings.Contains(blockA, "old thing") {
		t.Errorf("finished tasks are counted, not listed:\n%s", blockA)
	}

	b, err := Build(context.Background(), db, "pb", Options{})
	if err != nil {
		t.Fatal(err)
	}
	blockB := b.Block()
	if !strings.Contains(blockB, "Beta Lab") || strings.Contains(blockB, "Alpha Studio") || strings.Contains(blockB, "alice_handle") || strings.Contains(blockB, "github-token") {
		t.Errorf("profile b leaks profile a:\n%s", blockB)
	}
}

func TestNoSecretValueAppears(t *testing.T) {
	db := testDB(t)
	seed(t, db, "pa", "Alpha", t.TempDir())
	fill(t, db, "pa")
	c, err := Build(context.Background(), db, "pa", Options{})
	if err != nil {
		t.Fatal(err)
	}
	js, _ := json.Marshal(c)
	for _, out := range []string{c.Block(), string(js)} {
		for _, bad := range []string{secretValue, cookieSecret, "octo-user", "sessionid"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%q leaked into:\n%s", bad, out)
			}
		}
	}
}

func TestOrgsAreReadFromTheProfileFolder(t *testing.T) {
	db := testDB(t)
	root := t.TempDir()
	seed(t, db, "pa", "Alpha", root)
	dir := filepath.Join(root, ".monomind", "orgs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "growth.json"), []byte(`{"name":"growth"}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{}`), 0o644)
	c, err := Build(context.Background(), db, "pa", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Orgs.Count != 1 || c.Orgs.Recent[0] != "growth" {
		t.Fatalf("orgs: %+v", c.Orgs)
	}
}

func TestExtensionStatusIsInjected(t *testing.T) {
	db := testDB(t)
	seed(t, db, "pa", "Alpha", t.TempDir())
	c, err := Build(context.Background(), db, "pa", Options{Extension: func() Extension { return Extension{Status: "connected", Browsers: 1} }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Block(), "connected") {
		t.Errorf("extension status missing:\n%s", c.Block())
	}
}

func TestBlockNeverFails(t *testing.T) {
	db := testDB(t)
	if got := Block(context.Background(), db, "ghost"); got != "" {
		t.Errorf("unknown profile should give an empty block, got %q", got)
	}
	if got := Block(context.Background(), nil, "x"); got != "" {
		t.Errorf("nil db should give an empty block, got %q", got)
	}
}

func TestBlockNamesTheSecondBrainOnlyWhenSomethingIsIndexed(t *testing.T) {
	db := testDB(t)
	seed(t, db, "pa", "Alpha Studio", t.TempDir())
	fill(t, db, "pa")

	c, err := Build(context.Background(), db, "pa", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.Block(), "search-knowledge") {
		t.Errorf("nothing indexed, yet the block names the second brain:\n%s", c.Block())
	}

	mustExec(t, db, `UPDATE vault_documents SET indexed = 1 WHERE id = 'doc-001'`)
	c, err = Build(context.Background(), db, "pa", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if want := "Second brain: 1 indexed"; !strings.Contains(c.Block(), want) || !strings.Contains(c.Block(), "search-knowledge") {
		t.Errorf("block lacks %q and the search-knowledge hint:\n%s", want, c.Block())
	}
}
