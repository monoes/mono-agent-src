package chat

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/procbind"
)

// peercredWithRun is a database with binding on and one run of the given state.
func peercredWithRun(t *testing.T, state string) string {
	t.Helper()
	procbind.ForTestSetIssuance(t, false)
	asOperator(t)
	dbPath := newChatCLITestDB(t)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, PeercredBinding: true})
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p', 'P')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c1', ?, x'01')`, state); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

// OWNER DECISION (#1521): binding cannot be switched off while an open-ended run is open (its turns would be refused after the
// run started); the refusal points at `doomsday stop`, and the setting is unchanged. Mutant: dropping the guard (red here).
func TestPeercredBindingCannotBeSwitchedOffUnderAnOpenRun(t *testing.T) {
	dbPath := peercredWithRun(t, "running")
	out, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off")
	if code != 3 || !strings.Contains(out, "doomsday stop") {
		t.Fatalf("switching off under an open run: exit %d: %s", code, out)
	}
	if st, _ := runCoderCLI(t, dbPath, "status"); !strings.Contains(st, `"peercredBinding":true`) {
		t.Fatalf("the refused setter changed the setting: %s", st)
	}
}

// With the run stopped (quiescent) the setting can be switched off. Mutant: counting quiescent rows too (red here).
func TestPeercredBindingCanBeSwitchedOffAfterTheRunStopped(t *testing.T) {
	dbPath := peercredWithRun(t, "quiescent")
	if out, code := runCoderCLI(t, dbPath, "set", "--peercred-binding=off"); code != 0 {
		t.Fatalf("switching off after the run stopped: exit %d: %s", code, out)
	}
	if st, _ := runCoderCLI(t, dbPath, "status"); strings.Contains(st, `"peercredBinding":true`) {
		t.Fatalf("off did not clear the setting: %s", st)
	}
}
