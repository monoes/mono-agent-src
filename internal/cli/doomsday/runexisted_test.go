//go:build doomsday

package doomsday

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/governor/machine"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// The run directory is not the only record that a run was ever opened: the database holds an insert-only one, written when
// the run is entered. A run directory that was deleted (or never held a trace) therefore cannot turn an unreadable run table
// from "refuse" into "carry on" for an install that ran the mode, even when this process never read the run.

// A host that never read the run and finds the run directory gone or empty still refuses when the run table is unreadable.
func TestHostGateRunDirectoryWipedStillRefusesAfterARunWasOpened(t *testing.T) {
	for _, wipe := range []string{"empty", "removed"} {
		t.Run(wipe, func(t *testing.T) {
			s := newSeededGate(t)
			seedRun(t, s.st) // no secret, witness or counter on disk: the directory holds no trace
			if wipe == "removed" {
				if err := os.Remove(s.dir.Dir); err != nil {
					t.Fatal(err)
				}
			}
			if s.dir.HasRunTraces() {
				t.Fatal("setup: the directory must hold no trace")
			}
			s.renameRuns(t)
			if ran, err := s.dispatch(); !refused(err) || ran {
				t.Fatalf("a dispatch was made with a wiped run directory and an unreadable table: ran %v err %v", ran, err)
			}
		})
	}
}

// The governor reads the same record.
func TestGovernorRunDirectoryWipedStillRefusesAfterARunWasOpened(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	gw := &GovernorWire{DB: s.db.DB, Profile: "p1", Keys: store.NewHMACKeyer([]byte("k")), Runs: s.st, RunDir: s.dir,
		Sampler: machine.NewFake(machine.Step{Sample: machine.Healthy()})}
	s.renameRuns(t)
	if _, err := gw.For(bg); err == nil {
		t.Fatal("the governor carried on with a wiped run directory and an unreadable table after a run was opened")
	}
}

// A finished run still counts: the record outlives the run's state, and the process learns it while the database is
// readable, so a closed database later does not reopen the gate.
func TestHostGateFinishedRunIsRememberedWhenTheDatabaseCloses(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	if _, err := s.db.DB.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.DB.Exec(`UPDATE doomsday_runs SET state = 'quiescent'`); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.dispatch(); err != nil || !ran {
		t.Fatalf("setup: with no open run the dispatch is plain: %v", err)
	}
	s.db.DB.Close()
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made over a closed database after a run was opened: ran %v err %v", ran, err)
	}
}

// An install that never ran the mode keeps failing open: the record is empty.
func TestHostGateNoRunEverOpenedStillFailsOpenWithAMigratedDatabase(t *testing.T) {
	s := newSeededGate(t)
	s.renameRuns(t)
	if ran, err := s.dispatch(); err != nil || !ran {
		t.Fatalf("a dispatch was refused on an install that never opened a run: %v", err)
	}
	s.db.DB.Close()
	if ran, err := s.dispatch(); err != nil || !ran {
		t.Fatalf("a dispatch was refused over a closed database on an install that never opened a run: %v", err)
	}
}

// A process that read the record as "never opened" must read it again when the table later cannot be listed: another process
// may have opened the first run since, and a wiped run directory must not hide that.
func TestHostGateRecordReadAsNeverOpenedIsReadAgainWhenTheTableTurnsUnreadable(t *testing.T) {
	s := newSeededGate(t)
	if ran, err := s.dispatch(); err != nil || !ran {
		t.Fatalf("setup: with no run the dispatch is plain: %v", err)
	}
	seedRun(t, s.st) // another process opens the first run; this process has not listed it
	if s.dir.HasRunTraces() {
		t.Fatal("setup: the directory must hold no trace")
	}
	s.renameRuns(t)
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made after another process opened a run, with the table unreadable and the directory wiped: ran %v err %v", ran, err)
	}
}
