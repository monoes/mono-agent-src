package sqltime

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Every stored form orders by the instant it holds; text order does not.
func TestSortKeyOrdersEveryStoredFormByTime(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t (name TEXT, at TEXT)`); err != nil {
		t.Fatal(err)
	}
	zone := time.FixedZone("+0330", 3*3600+30*60)
	rows := []struct{ name, at string }{
		{"rfc3339-offset", "2026-10-19T12:34:59+03:30"},                           // 09:04:59Z
		{"millis", "2026-10-19T09:05:00.000Z"},                                    // 09:05:00Z
		{"go-string-zone", time.Date(2026, 10, 19, 12, 34, 57, 0, zone).String()}, // 09:04:57Z
		{"rfc3339", "2026-10-19T09:04:58Z"},
		{"current-timestamp", "2026-10-19 09:04:56"},
		{"go-string-utc", time.Date(2026, 10, 19, 9, 4, 55, 500_000_000, time.UTC).String()},
		{"go-string-utc-whole", time.Date(2026, 10, 19, 9, 4, 54, 0, time.UTC).String()},
		{"earlier-day-rfc3339", "2026-10-18T23:59:59Z"},
		{"null", ""},
	}
	for _, r := range rows {
		var at any = r.at
		if r.at == "" {
			at = nil
		}
		if _, err := db.Exec(`INSERT INTO t VALUES (?, ?)`, r.name, at); err != nil {
			t.Fatal(err)
		}
	}
	order := func(by string) string {
		t.Helper()
		rs, err := db.Query(`SELECT name FROM t ORDER BY ` + by)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		var names []string
		for rs.Next() {
			var n string
			if err := rs.Scan(&n); err != nil {
				t.Fatal(err)
			}
			names = append(names, n)
		}
		return strings.Join(names, ",")
	}
	want := "null,earlier-day-rfc3339,go-string-utc-whole,go-string-utc,current-timestamp,go-string-zone,rfc3339,rfc3339-offset,millis"
	if got := order(SortKey("at") + " ASC"); got != want {
		t.Errorf("SortKey order:\n got %s\nwant %s", got, want)
	}
	if got := order("at ASC"); got == want {
		t.Error("plain text order came out right too: the fixture no longer shows the problem")
	}
	if got := order(SortKey("COALESCE(at, '2026-10-20 00:00:00')") + " DESC"); !strings.HasPrefix(got, "null,millis,") {
		t.Errorf("SortKey of an expression: %s", got)
	}
}
