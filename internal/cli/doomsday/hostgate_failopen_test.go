package doomsday

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/storage"
)

func renameRuns(t *testing.T, r *hostRig) {
	t.Helper()
	if _, err := r.db.DB.Exec(`ALTER TABLE doomsday_runs RENAME TO moved_runs`); err != nil {
		t.Fatal(err)
	}
}

// No run was ever seen (no row read in this process, no per-run secret on disk): a doomsday_runs table that is missing or
// unreadable must not stop an ordinary dispatch.
func TestHostGateUnreadableTableWithNoRunFailsOpen(t *testing.T) {
	r := newHostRig(t)
	renameRuns(t, r)
	if err := r.startTurn(); err != nil || r.l.starts != 1 {
		t.Fatalf("a turn start was refused with a missing table and no run: %v", err)
	}
	if err := r.spawn(); refused(err) {
		t.Fatalf("a spawn was refused with a missing table and no run: %v", err)
	}
	if err := r.mint(); refused(err) {
		t.Fatalf("a token mint was refused with a missing table and no run: %v", err)
	}
}

func TestHostGateClosedDatabaseWithNoRunFailsOpen(t *testing.T) {
	r := newHostRig(t)
	r.db.DB.Close()
	if err := r.startTurn(); err != nil || r.l.starts != 1 {
		t.Fatalf("a turn start was refused with an unreadable database and no run: %v", err)
	}
}

// seededGate is a gate over a database whose run row was entered directly (no engine, so no pinned connection): the gate has
// read nothing yet, and the run directory holds no trace unless the test adds one.
type seededGate struct {
	db   *storage.Database
	st   *store.SQLStore
	g    *HostGate
	dir  stop.RunDir
	runs int
}

func seedRun(t *testing.T, st *store.SQLStore) {
	t.Helper()
	c := store.Charter{
		Profile: "p1", Generation: 1, Goal: "ship it",
		Criteria: []store.Criterion{{Kind: "command", Spec: "go test ./...", Expected: "ok"}},
		Limits:   store.Limits{TreeDepth: 4, LivePerTree: 30, SpawnRate: 6, OpenSessions: 50, MaxConcurrent: 30, MaxTotal: 100, BudgetMicro: 5_000_000},
		Lines:    []string{store.LineSpending, store.LineInstall},
		BossPin:  store.Pin{Provider: "anthropic", Model: "m1", Version: "v1", PurposePolicy: "judging-v1"}, VerifierPin: store.Pin{Provider: "anthropic", Model: "m2", Version: "v1", PurposePolicy: "judging-v1"},
		PerAsk: store.PerAsk{Declared: true, SpendMicro: 1_000_000, Tasks: 5, Access: "edit", Depth: 2, RunSpendMicro: 10_000_000, RunTasks: 50, AnswerClasses: []string{"context", "clarify"}},
	}
	if _, err := st.Enter(context.Background(), c, c.Hash(), "operator"); err != nil {
		t.Fatal(err)
	}
}

func newSeededGate(t *testing.T) *seededGate {
	t.Helper()
	keyring.MockInit()
	db, err := storage.NewDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	st := store.New(db.DB, store.NewHMACKeyer([]byte("k")))
	dir := stop.RunDir{Dir: t.TempDir() + "/run"}
	if err := os.MkdirAll(dir.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sg := &seededGate{db: db, st: st, dir: dir}
	sg.g = &HostGate{Store: st, Profile: "p1", RunDir: dir, BindingOn: func() (bool, error) { return true, nil },
		Fresh: func(context.Context, int64) error { return nil }, HostLock: func(stop.RunDir) (func(), error) { return func() {}, nil },
		Exit: func([]uint64) {}}
	return sg
}

// dispatch makes one turn start through the gate; ran says the create function was called.
type fakeProc struct{}

func (fakeProc) Handle() string { return "fake" }
func (fakeProc) Kill() error    { return nil }

func (s *seededGate) dispatch() (ran bool, err error) {
	err = s.g.Start(coderhost.PointTurnStart, func() (coderhost.StartedProc, error) { ran = true; return fakeProc{}, nil })
	return ran, err
}

func (s *seededGate) renameRuns(t *testing.T) {
	t.Helper()
	if _, err := s.db.DB.Exec(`ALTER TABLE doomsday_runs RENAME TO moved_runs`); err != nil {
		t.Fatal(err)
	}
}

// A run this host process has seen stays known: when its table later becomes unreadable every dispatch is refused.
func TestHostGateUnreadableTableAfterARunWasSeenRefuses(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	if ran, err := s.dispatch(); err != nil || !ran {
		t.Fatalf("setup: the dispatch inside the run must be made: %v", err)
	}
	s.renameRuns(t)
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made after the run state became unreadable: ran %v err %v", ran, err)
	}
	s.db.DB.Close()
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made over a closed database: ran %v err %v", ran, err)
	}
}

// A host that never read the run (it was entered by another process, or before this host restarted) still fails closed when
// the install has a run's per-run secret on disk, and the database cannot be read.
func TestHostGateUnreadableTableWithARunOnDiskRefuses(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	if err := s.dir.CreateSecret(1); err != nil { // what the engine does right after entering a run
		t.Fatal(err)
	}
	s.renameRuns(t)
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made with a run on disk and an unreadable table: ran %v err %v", ran, err)
	}
}

// A run row that exists and does not verify is a run, not an unreadable table: refused even with nothing on disk and
// nothing seen.
func TestHostGateUnverifiableRunRowRefuses(t *testing.T) {
	s := newSeededGate(t)
	seedRun(t, s.st)
	if _, err := s.db.DB.Exec(`DROP TRIGGER doomsday_runs_forward_only`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.DB.Exec(`UPDATE doomsday_runs SET cause = 'forged'`); err != nil {
		t.Fatal(err)
	}
	if ran, err := s.dispatch(); !refused(err) || ran {
		t.Fatalf("a dispatch was made over a run row that does not verify: ran %v err %v", ran, err)
	}
}

// The cgroup placer reads the run the same way: nothing to place when no run is known and the table cannot be listed, a refusal
// once a run was seen.
func TestHostGatePlacerFollowsTheSameRule(t *testing.T) {
	s := newSeededGate(t)
	s.renameRuns(t)
	release, err := s.g.PlaceRelease(exec.Command("true"), "p1", "conv")
	if err != nil || release == nil {
		t.Fatalf("a placement was refused with no run known: %v", err)
	}
	release()

	k := newSeededGate(t)
	seedRun(t, k.st)
	if _, err := k.dispatch(); err != nil { // the gate sees the run
		t.Fatal(err)
	}
	k.renameRuns(t)
	if _, err := k.g.PlaceRelease(exec.Command("true"), "p1", "conv"); err == nil {
		t.Fatal("a placement was made after the run state became unreadable")
	}
}
