package store

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/data"
)

// An install that opened a run before the record existed is filled from its runs (which are never deleted) when the migration
// is applied, and applying it again changes nothing.
func TestRunSeenMigrationBackfillsFromExistingRuns(t *testing.T) {
	s, db := newStore(t)
	enter(t, s, "p")
	if _, err := db.Exec("DROP TABLE doomsday_run_seen"); err != nil {
		t.Fatal(err)
	}
	raw, err := data.MigrationsFS.ReadFile("migrations/158_doomsday_run_seen.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		for _, stmt := range splitForTest(string(raw)) {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("pass %d: %v\n%s", i, err, stmt)
			}
		}
	}
	var n int
	var gen int64
	if err := db.QueryRow(`SELECT COUNT(*), MIN(first_generation) FROM doomsday_run_seen WHERE profile = 'p'`).Scan(&n, &gen); err != nil || n != 1 || gen != 1 {
		t.Fatalf("backfill: %d %d %v", n, gen, err)
	}
}

// splitForTest cuts the migration at its statement ends: a trigger body ends at "END;", everything else at ";".
func splitForTest(src string) []string {
	var out []string
	var cur strings.Builder
	inTrigger := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "--") {
			continue
		}
		cur.WriteString(line + "\n")
		if strings.Contains(strings.ToUpper(cur.String()), "CREATE TRIGGER") {
			inTrigger = true
		}
		if (inTrigger && strings.HasSuffix(strings.ToUpper(t), "END;")) || (!inTrigger && strings.HasSuffix(t, ";")) {
			out = append(out, cur.String())
			cur.Reset()
			inTrigger = false
		}
	}
	return out
}
