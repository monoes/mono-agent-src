package storage

import (
	"database/sql"
	"strings"
	"testing"
)

// A corpus of scripts SQLite accepts: each is split, every statement is executed on a real SQLite database, and
// the count says nothing was cut early or glued together. Quoted identifiers and comments hide `--`, `/*`,
// apostrophes, semicolons and the words CASE, BEGIN and END.
func TestSplitCorpusRunsOnSQLite(t *testing.T) {
	const table = `CREATE TABLE a (x INTEGER, y INTEGER, "a--b" INTEGER, "q""--b" INTEGER, "it's" INTEGER, [b--c] INTEGER, [it's2] INTEGER, ` + "`t--u`" + ` INTEGER, "semi;colon" INTEGER, "end" INTEGER, "case" INTEGER)`
	corpus := map[string]struct {
		script string
		want   int // statements after the table
	}{
		"dash dash in a quoted identifier": {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET "a--b" = 1; END;`, 1},
		"embedded quote and dashes":        {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET "q""--b" = 1; END;`, 1},
		"apostrophe in an identifier":      {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET "it's" = 1; UPDATE a SET x = 2; END;`, 1},
		"block comment with dashes":        {"CREATE TRIGGER t AFTER INSERT ON a BEGIN /* -- comment */ UPDATE a SET x = 1; /* it's; END; */ END;", 1},
		"bracket identifiers":              {"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET [b--c] = 1; UPDATE a SET [it's2] = 1; END;", 1},
		"backtick identifier":              {"CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET `t--u` = 1; END;", 1},
		"semicolon in an identifier":       {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET "semi;colon" = 1; END;`, 1},
		"keywords as quoted names":         {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET "end" = 1, "case" = 2; END;`, 1},
		"keywords in strings and comments": {"CREATE TRIGGER t AFTER INSERT ON a BEGIN -- END; BEGIN\n UPDATE a SET x = LENGTH('END; CASE BEGIN'); END;", 1},
		"nested case":                      {`CREATE TRIGGER t AFTER INSERT ON a WHEN CASE WHEN NEW.x > 0 THEN 1 ELSE 0 END = 1 BEGIN UPDATE a SET y = CASE WHEN x > 1 THEN CASE WHEN x > 2 THEN 3 ELSE 2 END ELSE 1 END; UPDATE a SET x = x; END;`, 1},
		"temporary if not exists":          {`CREATE TEMPORARY TRIGGER IF NOT EXISTS t AFTER INSERT ON a BEGIN UPDATE a SET x = 1; END;`, 1},
		"two triggers with CRLF":           {"CREATE TRIGGER t1 AFTER INSERT ON a\r\nBEGIN\r\n  UPDATE a SET x = 1;\r\nEND;\r\nCREATE TRIGGER t2 AFTER DELETE ON a\r\nBEGIN\r\n  UPDATE a SET y = 1;\r\nEND;\r\n", 2},
		"trigger then ordinary statements": {`CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET x = 1; END; CREATE INDEX ix ON a(x); CREATE INDEX iy ON a(y);`, 3},
		"quote then trigger":               {"INSERT INTO a (x) VALUES (1); /* ; */ CREATE TRIGGER t AFTER INSERT ON a BEGIN UPDATE a SET x = 2; END; INSERT INTO a (x) VALUES (3)", 3},
	}
	for name, tc := range corpus {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(table); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.script); err != nil { // SQLite itself takes the whole script
				t.Fatalf("SQLite refuses the script: %v", err)
			}
			stmts, err := splitStatementsChecked(tc.script)
			if err != nil || len(stmts) != tc.want {
				t.Fatalf("%d statements (want %d), %v: %q", len(stmts), tc.want, err, stmts)
			}
			fresh, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if _, err := fresh.Exec(table); err != nil {
				t.Fatal(err)
			}
			for _, s := range stmts {
				if _, err := fresh.Exec(s); err != nil {
					t.Fatalf("SQLite refuses the statement %q: %v", s, err)
				}
			}
			var n int
			if err := fresh.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if want := strings.Count(strings.ToUpper(tc.script), "CREATE TRIGGER") + strings.Count(strings.ToUpper(tc.script), "CREATE TEMPORARY TRIGGER"); n != want && !strings.Contains(strings.ToUpper(tc.script), "TEMPORARY") {
				t.Fatalf("%d triggers created, want %d", n, want)
			}
		})
	}
}
