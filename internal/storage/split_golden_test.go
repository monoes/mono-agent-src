package storage

import (
	"database/sql"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

// legacySplitStatements is splitStatements as it was before trigger bodies were kept whole (the master version,
// copied verbatim). The migration runner runs every user's database through splitStatements, so a change to it must
// leave every migration that has no trigger splitting exactly as before.
func legacySplitStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inSingleQuote := false
	inLineComment := false
	runes := []rune(sql)

	for i := 0; i < len(runes); i++ {
		ch := runes[i]
		if !inSingleQuote && ch == '-' && i+1 < len(runes) && runes[i+1] == '-' {
			inLineComment = true
			current.WriteRune(ch)
			continue
		}
		if inLineComment {
			current.WriteRune(ch)
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if ch == '\'' && !inLineComment {
			inSingleQuote = !inSingleQuote
			current.WriteRune(ch)
			continue
		}
		if ch == ';' && !inSingleQuote {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	if stmt := strings.TrimSpace(current.String()); stmt != "" {
		statements = append(statements, stmt)
	}
	return statements
}

// The golden comparison: every embedded migration splits exactly as it did before, except the ones that create a
// trigger (and only those, which this test names so that a new trigger migration is a deliberate change).
func TestSplitterLeavesEveryExistingMigrationUnchanged(t *testing.T) {
	files, err := fs.Glob(data.MigrationsFS, "migrations/*.sql")
	if err != nil || len(files) < 100 {
		t.Fatalf("found %d migrations (%v)", len(files), err)
	}
	var withTriggers []string
	for _, f := range files {
		raw, err := data.MigrationsFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		if strings.Contains(strings.ToUpper(src), "CREATE TRIGGER") || strings.Contains(strings.ToUpper(src), "CREATE TEMP TRIGGER") {
			withTriggers = append(withTriggers, f)
			continue
		}
		if got, want := splitStatements(src), legacySplitStatements(src); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the splitter changed how this migration splits (%d statements, was %d)", f, len(got), len(want))
		}
	}
	sort.Strings(withTriggers)
	if want := []string{"migrations/143_coder_budget_requests.sql"}; !reflect.DeepEqual(withTriggers, want) {
		t.Fatalf("migrations with triggers = %v, want %v: a new trigger migration needs this list and its split checked", withTriggers, want)
	}
}

// The one migration with triggers splits into whole statements that SQLite accepts, and nothing is left over.
func TestTriggerMigrationSplitsIntoStatementsSQLiteAccepts(t *testing.T) {
	raw, err := data.MigrationsFS.ReadFile("migrations/143_coder_budget_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitStatements(string(raw))
	triggers := 0
	for _, s := range stmts {
		if strings.Contains(strings.ToUpper(s), "CREATE TRIGGER") {
			triggers++
			if !strings.HasSuffix(strings.ToUpper(s), "END") {
				t.Errorf("a trigger was cut before its END: %q", s)
			}
		}
	}
	if triggers != 7 {
		t.Fatalf("%d triggers, want the 7 of the migration", triggers)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, pre := range []string{`CREATE TABLE coder_sessions (profile_id TEXT, conversation_id TEXT, budget_usd REAL, state TEXT)`,
		`CREATE TABLE task_sessions (id INTEGER, profile_id TEXT, state TEXT, archived_at TEXT)`,
		`CREATE TABLE coder_mission_children (profile_id TEXT, session_id INTEGER, task_id INTEGER, state TEXT, child_conversation_id TEXT)`} {
		if _, err := db.Exec(pre); err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("SQLite refused %q: %v", s, err)
		}
	}
}

// A trigger that never reaches its END is not silently dropped or glued onto the next statement: it comes out as
// one statement that SQLite refuses, so the migration fails loudly.
func TestUnterminatedTriggerFailsLoudly(t *testing.T) {
	script := `CREATE TABLE a (x INTEGER);
CREATE TRIGGER trg AFTER UPDATE ON a
BEGIN
    UPDATE a SET x = 1;
CREATE INDEX i ON a(x);`
	stmts := splitStatements(script)
	if len(stmts) != 2 || !strings.HasPrefix(stmts[1], "CREATE TRIGGER") || !strings.Contains(stmts[1], "CREATE INDEX i") {
		t.Fatalf("statements = %q", stmts)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmts[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(stmts[1]); err == nil {
		t.Fatal("SQLite accepted a trigger with no END")
	}
}

// Words that merely contain END, quotes and comments do not close or open a trigger.
func TestTriggerSplitIgnoresLookalikes(t *testing.T) {
	script := `CREATE TABLE legend (x TEXT);
-- CREATE TRIGGER in a comment; still a comment
CREATE INDEX i ON legend(x);
INSERT INTO legend VALUES ('CREATE TRIGGER x BEGIN');
CREATE TRIGGER t AFTER INSERT ON legend BEGIN
    INSERT INTO legend VALUES ('weekend');
    INSERT INTO legend VALUES ('the END');
END;
CREATE TABLE after_trigger (y INTEGER);`
	stmts := splitStatements(script)
	if len(stmts) != 5 {
		t.Fatalf("%d statements: %q", len(stmts), stmts)
	}
	if !strings.HasPrefix(stmts[3], "CREATE TRIGGER t") || !strings.HasSuffix(stmts[3], "END") || !strings.HasPrefix(stmts[4], "CREATE TABLE after_trigger") {
		t.Fatalf("statements = %q", stmts)
	}
}
