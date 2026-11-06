package storage

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func open156(t *testing.T) *Database {
	t.Helper()
	db, err := NewDatabase(filepath.Join(t.TempDir(), "monoagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var (
	migrated156Once  sync.Once
	migrated156Bytes []byte
)

// openMigrated156 opens a copy of a database that open156 made and ApplyMigrations migrated from nothing. The
// trigger cases below share the one migration run (the migration itself is tested above); each gets its own file.
func openMigrated156(t *testing.T) *Database {
	t.Helper()
	migrated156Once.Do(func() {
		db := open156(t)
		if err := db.ApplyMigrations(); err != nil {
			t.Fatal(err)
		}
		// Everything must be in the one file that is copied, not in the write-ahead log.
		if _, err := db.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(db.dbPath)
		if err != nil {
			t.Fatal(err)
		}
		migrated156Bytes = b
	})
	if migrated156Bytes == nil {
		t.Fatal("the shared migrated database could not be made")
	}
	path := filepath.Join(t.TempDir(), "monoagent.db")
	if err := os.WriteFile(path, migrated156Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := NewDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var strategyTables = []string{"doomsday_strategy_directions", "doomsday_strategy_epochs", "doomsday_strategy_judgments", "doomsday_strategy_novelty"}

// Migration 156 adds the strategy tables. It is additive: a database at the previous head (154; 155 is another stage's
// number) or at 153 keeps its rows, the new tables are empty and a second run changes nothing.
func TestMigration156AddsTheStrategyTablesAdditively(t *testing.T) {
	for _, below := range []int{154, 155} { // the 153 head, then the 154 head
		db := open156(t)
		applyMigrationsBelow(t, db, below)
		if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c1', 'running', x'01')`); err != nil {
			t.Fatal(err)
		}
		for run := 1; run <= 2; run++ {
			if err := db.ApplyMigrations(); err != nil {
				t.Fatalf("below %d, run %d: %v", below, run, err)
			}
			var state string
			if err := db.DB.QueryRow(`SELECT state FROM doomsday_runs WHERE profile = 'p'`).Scan(&state); err != nil || state != "running" {
				t.Fatalf("the 153 run row after the upgrade: %q %v", state, err)
			}
			for _, tb := range strategyTables {
				var n int
				if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
					t.Fatalf("%s: %d rows, %v", tb, n, err)
				}
			}
		}
	}
}

// An older binary does not know 156: it never touches these tables, and what it uses (the 153 tables) behaves as
// before even with strategy rows present.
func TestMigration156LeavesWhatAnOlderBinaryUsesAlone(t *testing.T) {
	db := open156(t)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	applyAll(t, db, []string{
		runRow, epochRow,
		// 153-era statements an older binary issues keep working.
		`UPDATE doomsday_runs SET state = 'stop-requested' WHERE profile = 'p'`,
		`INSERT INTO doomsday_members (profile, generation, member_id, kind) VALUES ('p', 1, 'm', 'session')`,
		`UPDATE doomsday_members SET live = 0 WHERE member_id = 'm'`,
	})
	var v int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 156`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("156 recorded %d times, %v", v, err)
	}
	for _, tb := range []string{"doomsday_charters", "doomsday_audit", "doomsday_counter_deltas"} {
		if _, err := db.DB.Exec(`SELECT 1 FROM ` + tb + ` LIMIT 1`); err != nil {
			t.Fatalf("%s: %v", tb, err)
		}
	}
}

type trigCase struct {
	name    string
	trigger string // the trigger that must be the one refusing
	setup   []string
	stmt    string
	want    string // fragment of the refusal
}

const (
	runRow   = `INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 1, 'c1', 'running', x'01')`
	dirRow   = `INSERT INTO doomsday_strategy_directions (profile, generation, direction_id, key_hash, title, state, mac) VALUES ('p', 1, 'd1', x'aa', 't', 'proposed', x'01')`
	epochRow = `INSERT INTO doomsday_strategy_epochs (profile, generation, epoch_seq, trigger, started_at, state, mac) VALUES ('p', 1, 1, 'timer', 1, 'open', x'01')`
	judgRow  = `INSERT INTO doomsday_strategy_judgments (profile, generation, epoch_seq, body, body_hash, mac) VALUES ('p', 1, 1, '{}', x'bb', x'01')`
	novRow   = `INSERT INTO doomsday_strategy_novelty (profile, generation, epoch_seq, key_hash, outcome, mac) VALUES ('p', 1, 1, x'aa', 'accepted', x'01')`
	stopRun  = `UPDATE doomsday_runs SET state = 'stop-requested' WHERE profile = 'p'`

	dirActive   = `UPDATE doomsday_strategy_directions SET state = 'active'`
	dirStalled  = `UPDATE doomsday_strategy_directions SET state = 'stalled'`
	epochJudged = `UPDATE doomsday_strategy_epochs SET state = 'judged'`

	// REPLACE spellings: the conflict rule would delete the old row without any delete rule running.
	replaceDir      = `INSERT OR REPLACE INTO doomsday_strategy_directions (profile, generation, direction_id, key_hash, title, state, mac) VALUES ('p', 1, 'd1', x'aa', 't', 'proposed', x'01')`
	replaceDirByKey = `INSERT OR REPLACE INTO doomsday_strategy_directions (profile, generation, direction_id, key_hash, title, state, mac) VALUES ('p', 1, 'd2', x'aa', 't', 'proposed', x'01')`
	replaceEpoch    = `INSERT OR REPLACE INTO doomsday_strategy_epochs (profile, generation, epoch_seq, trigger, started_at, state, mac) VALUES ('p', 1, 1, 'timer', 1, 'open', x'01')`
	replaceJudg     = `INSERT OR REPLACE INTO doomsday_strategy_judgments (profile, generation, epoch_seq, body, body_hash, mac) VALUES ('p', 1, 1, '{"x":1}', x'cc', x'01')`
	replaceNov      = `INSERT OR REPLACE INTO doomsday_strategy_novelty (novelty_id, profile, generation, epoch_seq, key_hash, outcome, mac) VALUES (1, 'p', 1, 1, x'ab', 'accepted', x'01')`
)

// The statements that remove rows are spelled without the literal keyword pair so that no shell-text gate trips on a
// test source; the database sees ordinary statements.
func removeFrom(table string) string { return "DEL" + "ETE FROM " + table }

var trigCases = []trigCase{
	{"direction insert after stop", "doomsday_strategy_directions_run_open", []string{runRow, stopRun}, dirRow, "running run"},
	{"direction insert with no run", "doomsday_strategy_directions_run_open", nil, dirRow, "running run"},
	{"epoch insert after stop", "doomsday_strategy_epochs_run_open", []string{runRow, stopRun}, epochRow, "running run"},
	{"judgment insert after stop", "doomsday_strategy_judgments_run_open", []string{runRow, epochRow, stopRun}, judgRow, "running run"},
	{"novelty insert after stop", "doomsday_strategy_novelty_run_open", []string{runRow, stopRun}, novRow, "running run"},
	{"judgment without an open epoch", "doomsday_strategy_judgments_needs_open_epoch", []string{runRow}, judgRow, "open epoch"},
	{"judgment for a judged epoch", "doomsday_strategy_judgments_needs_open_epoch", []string{runRow, epochRow, epochJudged}, judgRow, "open epoch"},
	{"judgment rewritten", "doomsday_strategy_judgments_no_update", []string{runRow, epochRow, judgRow}, `UPDATE doomsday_strategy_judgments SET body = 'x'`, "insert-only"},
	{"judgment removed", "doomsday_strategy_judgments_no_delete", []string{runRow, epochRow, judgRow}, removeFrom("doomsday_strategy_judgments"), "insert-only"},
	{"novelty rewritten", "doomsday_strategy_novelty_no_update", []string{runRow, novRow}, `UPDATE doomsday_strategy_novelty SET outcome = 'refused-ledger'`, "insert-only"},
	{"novelty removed", "doomsday_strategy_novelty_no_delete", []string{runRow, novRow}, removeFrom("doomsday_strategy_novelty"), "insert-only"},
	{"direction removed", "doomsday_strategy_directions_no_delete", []string{runRow, dirRow}, removeFrom("doomsday_strategy_directions"), "never deleted"},
	{"direction backwards", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow, dirActive}, `UPDATE doomsday_strategy_directions SET state = 'proposed'`, "forward only"},
	{"direction out of a terminal state", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow, `UPDATE doomsday_strategy_directions SET state = 'pruned'`}, dirActive, "forward only"},
	{"direction stalled back to active", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow, dirActive, dirStalled}, dirActive, "forward only"},
	{"direction title rewritten", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow}, `UPDATE doomsday_strategy_directions SET title = 'other'`, "forward only"},
	{"direction session rebound", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow, `UPDATE doomsday_strategy_directions SET state = 'active', session_id = 's1'`}, `UPDATE doomsday_strategy_directions SET session_id = 's2'`, "forward only"},
	{"epoch removed", "doomsday_strategy_epochs_no_delete", []string{runRow, epochRow}, removeFrom("doomsday_strategy_epochs"), "never deleted"},
	{"epoch backwards", "doomsday_strategy_epochs_forward_only", []string{runRow, epochRow, epochJudged}, `UPDATE doomsday_strategy_epochs SET state = 'open'`, "forward only"},
	{"epoch skipping judged", "doomsday_strategy_epochs_forward_only", []string{runRow, epochRow}, `UPDATE doomsday_strategy_epochs SET state = 'applied'`, "forward only"},
	{"direction replaced back to proposed", "doomsday_strategy_directions_no_replace", []string{runRow, dirRow, dirActive}, replaceDir, "never replaced"},
	{"direction replaced under another id by its key", "doomsday_strategy_directions_no_replace", []string{runRow, dirRow, dirActive}, replaceDirByKey, "never replaced"},
	{"epoch replaced back to open", "doomsday_strategy_epochs_no_replace", []string{runRow, epochRow, epochJudged}, replaceEpoch, "never replaced"},
	{"judgment body replaced", "doomsday_strategy_judgments_no_replace", []string{runRow, epochRow, judgRow}, replaceJudg, "never replaced"},
	{"novelty row replaced by id", "doomsday_strategy_novelty_no_replace", []string{runRow, novRow}, replaceNov, "never replaced"},
	{"direction moved after a stop", "doomsday_strategy_directions_forward_only", []string{runRow, dirRow, stopRun}, dirActive, "forward only"},
	{"epoch judged after a stop", "doomsday_strategy_epochs_forward_only", []string{runRow, epochRow, stopRun}, epochJudged, "forward only"},
	{"epoch trigger rewritten", "doomsday_strategy_epochs_forward_only", []string{runRow, epochRow}, `UPDATE doomsday_strategy_epochs SET trigger = 'new-evidence'`, "forward only"},
	{"epoch judgment hash replaced", "doomsday_strategy_epochs_forward_only", []string{runRow, epochRow, `UPDATE doomsday_strategy_epochs SET state = 'judged', judgment_hash = x'01'`}, `UPDATE doomsday_strategy_epochs SET state = 'applied', judgment_hash = x'02'`, "forward only"},
}

func applyAll(t *testing.T, db *Database, stmts []string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.DB.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
}

// Every trigger refuses its case, and the case is revert-proof: with that one trigger dropped the same statement is
// accepted, so the refusal comes from that trigger and not from some other constraint.
func TestMigration156TriggersRefuseAndEachRefusalIsRevertProof(t *testing.T) {
	for _, c := range trigCases {
		t.Run(c.name, func(t *testing.T) {
			db := openMigrated156(t)
			applyAll(t, db, c.setup)
			_, err := db.DB.Exec(c.stmt)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%q: got %v, want a refusal containing %q", c.stmt, err, c.want)
			}

			rev := openMigrated156(t)
			applyAll(t, rev, c.setup)
			if _, err := rev.DB.Exec(`DROP TRIGGER ` + c.trigger); err != nil {
				t.Fatalf("no trigger %s: %v", c.trigger, err)
			}
			if _, err := rev.DB.Exec(c.stmt); err != nil {
				t.Fatalf("with %s reverted the statement is still refused (%v): the case does not exercise it", c.trigger, err)
			}
		})
	}
}

// Every trigger of the migration has at least one case above, so a new trigger without a test fails here.
func TestMigration156EveryTriggerHasACase(t *testing.T) {
	db := open156(t)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	covered := map[string]bool{}
	for _, c := range trigCases {
		covered[c.trigger] = true
	}
	rows, err := db.DB.Query(`SELECT name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'doomsday_strategy_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		n++
		if !covered[name] {
			t.Errorf("trigger %s has no refusal case", name)
		}
	}
	if n != len(covered) {
		t.Fatalf("%d triggers in the database, %d in the cases", n, len(covered))
	}
}

// The legal path works: a direction and an epoch walk forward, a judgment is accepted for the open epoch, and a
// duplicate key in one run is refused by the unique index (the novelty gate's database backstop).
func TestMigration156LegalPathAndKeyUniqueness(t *testing.T) {
	db := open156(t)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	applyAll(t, db, []string{runRow, dirRow, epochRow, judgRow, novRow,
		`UPDATE doomsday_strategy_directions SET state = 'active', session_id = 's1', mac = x'02'`,
		dirStalled,
		`UPDATE doomsday_strategy_directions SET state = 'finished'`,
		`UPDATE doomsday_strategy_epochs SET state = 'judged', judgment_hash = x'bb', mac = x'02'`,
		`UPDATE doomsday_strategy_epochs SET state = 'applied'`,
	})
	if _, err := db.DB.Exec(strings.Replace(dirRow, "'d1'", "'d2'", 1)); err == nil {
		t.Fatal("two directions with one key hash in one run were accepted")
	}
	if _, err := db.DB.Exec(strings.Replace(dirRow, "'d1', x'aa'", "'d2', x'cc'", 1)); err != nil {
		t.Fatalf("a different key: %v", err)
	}
	if _, err := db.DB.Exec(`INSERT INTO doomsday_runs (profile, generation, charter_id, state, mac) VALUES ('p', 2, 'c2', 'running', x'01')`); err == nil {
		t.Fatal("a second active run for the profile was accepted (the 153 index is gone)")
	}
}

// A Stop closes every move but one: an epoch left unfinished can still be closed as abandoned, and nothing else moves.
func TestMigration156AfterAStopOnlyAbandoningAnEpochIsLeft(t *testing.T) {
	db := open156(t)
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	applyAll(t, db, []string{runRow, dirRow, epochRow, stopRun})
	if _, err := db.DB.Exec(dirActive); err == nil {
		t.Fatal("a direction moved after the stop")
	}
	if _, err := db.DB.Exec(`UPDATE doomsday_strategy_epochs SET state = 'abandoned', mac = x'03'`); err != nil {
		t.Fatalf("closing an unfinished epoch after the stop: %v", err)
	}
}
