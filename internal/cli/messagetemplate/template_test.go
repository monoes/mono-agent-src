package messagetemplate

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// newTestDB is a migrated database in a temporary directory.
func newTestDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "templates.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

// testDeps runs the commands against the database at dbPath, with --json when jsonOut is set.
func testDeps(dbPath string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}

func TestTemplateListJSONIsAnArray(t *testing.T) {
	dbPath := newTestDB(t)
	run := func() string {
		var runErr error
		out := captureStdout(t, func() {
			cmd := NewCommand(testDeps(dbPath, true))
			cmd.SetArgs([]string{"list"})
			runErr = cmd.Execute()
		})
		if runErr != nil {
			t.Fatal(runErr)
		}
		return out
	}
	if out := strings.TrimSpace(run()); out != "[]" {
		t.Fatalf("empty template list = %q, want []", out)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO templates (name, subject, body) VALUES ('Hello', 'Hi', 'Body text')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var got []map[string]interface{}
	if err := json.Unmarshal([]byte(run()), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["name"] != "Hello" || got[0]["subject"] != "Hi" || got[0]["body"] != "Body text" || got[0]["id"] == nil {
		t.Fatalf("template list = %+v", got)
	}
}

// TestTemplateListTableRightAlignsNumericIDColumn guards against the same
// tablewriter v1 migration regression covered by login_test.go's
// TestLoginStatusTableRightAlignsNumericIDColumn: v0.0.5 auto-right-aligned
// any purely-numeric cell with no explicit SetColumnAlignment call; v1's
// shared clicore.NewPlainTable helper has no such content-sniffing default, so
// `template list`'s numeric ID column silently went from right- to
// left-aligned. Seeds two templates with explicit, deliberately
// different-width IDs (1 vs 100, via direct SQL rather than the `create`
// command, which can't control the autoincrement value) and asserts the
// shorter ID is left-padded to align with the wider one.
func TestTemplateListTableRightAlignsNumericIDColumn(t *testing.T) {
	dbPath := newTestDB(t)
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	now := time.Now().UTC()
	insert := func(id int, name string) {
		t.Helper()
		if _, err := db.DB.Exec(
			`INSERT INTO templates (id, name, subject, body, created_at, updated_at) VALUES (?, ?, '', 'body', ?, ?)`,
			id, name, now, now,
		); err != nil {
			t.Fatalf("seeding template id=%d: %v", id, err)
		}
	}
	insert(1, "shortid")
	insert(100, "longid")
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}

	cmd := NewCommand(testDeps(dbPath, false))
	cmd.SetArgs([]string{"list"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("template list: %v", err)
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
		t.Fatalf("expected both template rows in output, got:\n%s", out.String())
	}
	pos1 := strings.Index(line1, "1")
	pos100 := strings.Index(line100, "100")
	if pos1 <= pos100 {
		t.Fatalf("expected the single-digit ID to start further right than the 3-digit ID (right-alignment), got ID \"1\" at column %d and \"100\" at column %d\nrow1:   %q\nrow100: %q", pos1, pos100, line1, line100)
	}
}
