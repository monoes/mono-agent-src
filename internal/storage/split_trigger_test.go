package storage

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A trigger body may hold CASE ... END expressions; only the END that closes the BEGIN ends the trigger.
func TestTriggerWithCaseExpressionsStaysWhole(t *testing.T) {
	script := `CREATE TABLE a (x INTEGER, y INTEGER);
CREATE TRIGGER trg AFTER UPDATE ON a
WHEN CASE WHEN NEW.x > 0 THEN 1 ELSE 0 END = 1
BEGIN
    UPDATE a SET y = CASE WHEN NEW.x > 1 THEN CASE WHEN NEW.x > 2 THEN 3 ELSE 2 END ELSE 1 END WHERE x = NEW.x;
    UPDATE a SET x = x WHERE y = 0;
END;
CREATE INDEX i ON a(x);`
	stmts, err := splitStatementsChecked(script)
	if err != nil || len(stmts) != 3 {
		t.Fatalf("%d statements, %v: %q", len(stmts), err, stmts)
	}
	if !strings.HasPrefix(stmts[1], "CREATE TRIGGER trg") || !strings.HasSuffix(stmts[1], "END") || strings.Count(stmts[1], ";") != 2 {
		t.Fatalf("trigger = %q", stmts[1])
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("SQLite refused %q: %v", s, err)
		}
	}
}

// Words in comments, quoted strings, quoted identifiers and block comments never count as CASE, BEGIN or END.
func TestTriggerWordsInCommentsAndQuotesDoNotCount(t *testing.T) {
	script := "CREATE TABLE a (x INTEGER, \"end\" INTEGER);\n" +
		"CREATE TRIGGER trg AFTER UPDATE ON a -- END; not yet\n" +
		"BEGIN -- END\n" +
		"    /* END; */ UPDATE a SET \"end\" = 1 WHERE x = 'END;'; -- END;\n" +
		"    UPDATE a SET [end] = 2 WHERE `end` = 3;\n" +
		"END; -- trailing\n" +
		"CREATE INDEX i ON a(x);"
	stmts, err := splitStatementsChecked(script)
	if err != nil || len(stmts) != 3 {
		t.Fatalf("%d statements, %v: %q", len(stmts), err, stmts)
	}
	if !strings.HasPrefix(stmts[1], "CREATE TRIGGER trg") || !strings.HasSuffix(stmts[1], "END") || !strings.Contains(stmts[1], "[end] = 2") {
		t.Fatalf("trigger = %q", stmts[1])
	}
}

// The `CASE ... END;` counterexample from review: an END that closes a CASE does not end the trigger.
func TestTriggerStatementEndingInACaseEndDoesNotSplit(t *testing.T) {
	script := `CREATE TRIGGER t AFTER INSERT ON a BEGIN
    UPDATE a SET x = CASE WHEN 1 THEN 2 ELSE 3 END;
    UPDATE a SET x = 0;
END;`
	stmts, err := splitStatementsChecked(script)
	if err != nil || len(stmts) != 1 {
		t.Fatalf("%d statements, %v: %q", len(stmts), err, stmts)
	}
}

// A trigger that never closes is an explicit error of the checked splitter (the migration runner refuses the
// script before executing any of it), not something SQLite is left to notice.
func TestUnterminatedTriggerIsAnExplicitError(t *testing.T) {
	for name, script := range map[string]string{
		"no END":             "CREATE TABLE a (x INTEGER);\nCREATE TRIGGER t AFTER INSERT ON a BEGIN\n    UPDATE a SET x = 1;\nCREATE INDEX i ON a(x);",
		"only a CASE END":    "CREATE TRIGGER t AFTER INSERT ON a BEGIN\n    UPDATE a SET x = CASE WHEN 1 THEN 2 END;",
		"END only in a note": "CREATE TRIGGER t AFTER INSERT ON a BEGIN\n    UPDATE a SET x = 1; -- END;\n",
	} {
		if _, err := splitStatementsChecked(script); err == nil || !strings.Contains(err.Error(), "CREATE TRIGGER") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := splitStatementsChecked("CREATE TABLE a (x INTEGER);\nCREATE INDEX i ON a(x);"); err != nil {
		t.Fatal(err)
	}
}

// The budget-request triggers live on tables other migrations may rebuild, and a rebuild drops the triggers of
// the table. So the check is on the schema the migrations leave, not on their text: after all of them, the
// database has each of the triggers, on the table it belongs to.
func TestBudgetRequestTriggersSurviveEveryMigration(t *testing.T) {
	db, err := NewDatabase(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"trg_coder_budget_requests_pinned":        "coder_budget_requests",
		"trg_coder_budget_requests_once":          "coder_budget_requests",
		"trg_coder_budget_requests_session_moved": "coder_sessions",
		"trg_coder_budget_requests_session_gone":  "coder_sessions",
		"trg_coder_budget_requests_mission_ended": "task_sessions",
		"trg_coder_budget_requests_mission_gone":  "task_sessions",
		"trg_coder_budget_requests_child_moved":   "coder_mission_children",
		"trg_coder_budget_requests_child_gone":    "coder_mission_children",
	}
	rows, err := db.DB.Query(`SELECT name, tbl_name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'trg_coder_budget_requests_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, tbl string
		if err := rows.Scan(&name, &tbl); err != nil {
			t.Fatal(err)
		}
		got[name] = tbl
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("triggers after all migrations = %v, want %v: a migration that rebuilds a table has to recreate its triggers", got, want)
	}
}

// A script that ends inside a string, a quoted identifier or a block comment is an error, not a quiet last
// statement; a line comment at the very end is fine.
func TestScriptEndingInsideAQuoteOrCommentIsAnError(t *testing.T) {
	for name, script := range map[string]string{
		"block comment":       "CREATE TABLE a (x INTEGER); /* never closed",
		"block comment w/ ;":  "CREATE TABLE a (x INTEGER); /* never; closed",
		"string":              "CREATE TABLE a (x INTEGER); INSERT INTO a VALUES ('never closed);",
		"double quoted ident": `CREATE TABLE a (x INTEGER); SELECT "never closed;`,
		"backtick ident":      "CREATE TABLE a (x INTEGER); SELECT `never closed;",
		"bracket ident":       "CREATE TABLE a (x INTEGER); SELECT [never closed;",
		"inside a trigger":    "CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET x = 'open; END;",
	} {
		if _, err := splitStatementsChecked(script); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	for name, script := range map[string]string{
		"line comment at the end":    "CREATE TABLE a (x INTEGER); -- the end",
		"closed block comment":       "CREATE TABLE a (x INTEGER); /* closed */",
		"escaped quotes at the end":  "INSERT INTO a VALUES ('it''s'); SELECT \"a\"\"b\"",
		"slash star in a string":     "INSERT INTO a VALUES ('/* not a comment');",
		"dashes after a closed note": "CREATE TABLE a (x INTEGER); /* a */ -- b",
	} {
		if _, err := splitStatementsChecked(script); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
