package storage

import (
	"database/sql"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
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

// The budget-request triggers live on tables other migrations may rebuild. A rebuild drops the triggers of the
// table, so a migration after 143 that drops or renames one of these tables has to create the triggers again.
func TestLaterMigrationsThatRebuildATriggerTableRecreateItsTriggers(t *testing.T) {
	needs := map[string][]string{
		"coder_sessions":         {"trg_coder_budget_requests_session_moved", "trg_coder_budget_requests_session_gone"},
		"task_sessions":          {"trg_coder_budget_requests_mission_ended", "trg_coder_budget_requests_mission_gone"},
		"coder_mission_children": {"trg_coder_budget_requests_child_moved", "trg_coder_budget_requests_child_gone"},
	}
	files, err := fs.Glob(data.MigrationsFS, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	numRe := regexp.MustCompile(`migrations/(\d+)_`)
	for _, f := range files {
		m := numRe.FindStringSubmatch(f)
		if m == nil {
			continue
		}
		if n, _ := strconv.Atoi(m[1]); n <= 143 {
			continue
		}
		raw, err := data.MigrationsFS.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(raw)
		for table, triggers := range needs {
			rebuild := regexp.MustCompile(`(?i)(drop\s+table\s+(if\s+exists\s+)?` + table + `\b|alter\s+table\s+` + table + `\s+rename\s+to\b|alter\s+table\s+\w+\s+rename\s+to\s+` + table + `\b)`)
			if !rebuild.MatchString(src) {
				continue
			}
			for _, trg := range triggers {
				if !strings.Contains(src, trg) {
					t.Errorf("%s rebuilds %s without recreating %s: the budget-request triggers would be lost", f, table, trg)
				}
			}
		}
	}
}

// Every trigger the budget-request migration names for the guard above exists in it.
func TestBudgetRequestTriggersNamedByTheGuardExist(t *testing.T) {
	raw, err := data.MigrationsFS.ReadFile("migrations/143_coder_budget_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, trg := range []string{"session_moved", "session_gone", "mission_ended", "mission_gone", "child_moved", "child_gone"} {
		if !strings.Contains(string(raw), "trg_coder_budget_requests_"+trg) {
			t.Errorf("migration 143 has no trigger %s", trg)
		}
	}
}
