package inputs

import (
	"math"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit/strategy"
)

func TestSnapshotOfAStoppedPursuitCarriesNoData(t *testing.T) {
	f := newFixture(t)
	f.addTask(t, "ready", 0)
	s, err := f.reader.Snapshot(bg, f.session)
	if err != nil {
		t.Fatal(err)
	}
	if s.Run.Running || s.Run.Why != ReasonNotStarted || s.Tasks.Total != 0 || s.EvidenceSeq != 0 {
		t.Errorf("not started: %+v", s)
	}
	f.startPursuit(t)
	f.pause(t)
	s, _ = f.reader.Snapshot(bg, f.session)
	if s.Run.Running || s.Tasks.Total != 0 {
		t.Errorf("paused: %+v", s)
	}
}

func TestSnapshotUnreadableIsAnErrorAndNotRunning(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	f.db.Close()
	s, err := f.reader.Snapshot(bg, f.session)
	if err == nil || s.Run.Running {
		t.Errorf("closed db: %+v %v", s, err)
	}
}

func TestTaskCountsPerState(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	for _, st := range []string{"inbox", "ready", "ready", "in_progress", "review", "done", "done", "done", "archived"} {
		f.addTask(t, st, 0)
	}
	f.addTask(t, "in_progress", 1)
	other := addSession(t, f.db, "running", "mission")
	mustExec(t, f.db, `INSERT INTO tasks (profile_id, title, status, position, session_id, created_at, updated_at) VALUES ('default','t','ready',1,?, 'x','x')`, other)
	s, err := f.reader.Snapshot(bg, f.session)
	if err != nil || !s.Run.Running {
		t.Fatalf("%+v %v", s, err)
	}
	want := TaskCounts{Total: 10, Inbox: 1, Ready: 2, InProgress: 2, Review: 1, Done: 3, Archived: 1, Blocked: 1}
	if s.Tasks != want {
		t.Errorf("tasks = %+v, want %+v", s.Tasks, want)
	}
	if s.Tasks.Open() != 6 {
		t.Errorf("open = %d, want 6", s.Tasks.Open())
	}
}

func TestBudget(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	set := func(b, sp float64) {
		mustExec(t, f.db, `UPDATE task_sessions SET budget_usd=?, spent_usd=? WHERE id=?`, b, sp, f.session)
	}
	get := func() BudgetShare {
		s, err := f.reader.Snapshot(bg, f.session)
		if err != nil || !s.Run.Running {
			t.Fatalf("%+v %v", s, err)
		}
		return s.Budget
	}
	set(0, 3)
	if b := get(); b.Known || b.Share != 0 {
		t.Errorf("unknown budget (0): %+v", b)
	}
	set(-5, 1)
	if b := get(); b.Known {
		t.Errorf("negative budget: %+v", b)
	}
	set(10, -1)
	if b := get(); b.Known {
		t.Errorf("negative spend: %+v", b)
	}
	set(10, 2.5)
	if b := get(); !b.Known || b.Share != 0.25 || b.BudgetUSD != 10 || b.SpentUSD != 2.5 {
		t.Errorf("known: %+v", b)
	}
	set(10, 25)
	if b := get(); !b.Known || b.Share != 2.5 {
		t.Errorf("over budget keeps its share: %+v", b)
	}
	mustExec(t, f.db, `UPDATE task_sessions SET budget_usd=?, spent_usd=1 WHERE id=?`, math.Inf(1), f.session)
	if b := get(); b.Known {
		t.Errorf("infinite budget: %+v", b)
	}
}

func TestDirectionsAndEpochsComeFromTheStrategyStore(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	st := strategy.NewStore(f.db)
	prof := itoa(f.session)
	s, _ := f.reader.Snapshot(bg, f.session)
	if len(s.Directions) != 0 || s.Epochs.Count != 0 || s.Epochs.HasLatest {
		t.Errorf("empty: %+v", s)
	}
	if _, _, err := st.InsertEpoch(bg, types.Epoch{Profile: prof, Generation: 1, Seq: 1, Trigger: types.TriggerTimer, StartedAt: 100, ModelPin: "fake/judge/1", State: types.EpochOpen}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.InsertEpoch(bg, types.Epoch{Profile: prof, Generation: 1, Seq: 2, Trigger: types.TriggerFinishParked, StartedAt: 200, ModelPin: "fake/judge/1", State: types.EpochOpen}); err != nil {
		t.Fatal(err)
	}
	d := types.Direction{Profile: prof, Generation: 1, ID: "d1", KeyHash: [32]byte{1}, Title: "t", State: types.DirProposed, CreatedEpoch: 2}
	if err := st.AcceptProposal(bg, d, types.NoveltyRow{Profile: prof, Generation: 1, EpochSeq: 2, KeyHash: [32]byte{1}, DirectionID: "d1", Outcome: types.NoveltyAccepted}); err != nil {
		t.Fatal(err)
	}
	s, err := f.reader.Snapshot(bg, f.session)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Directions) != 1 || s.Directions[0].ID != "d1" {
		t.Errorf("directions = %+v", s.Directions)
	}
	if s.Epochs.Count != 2 || !s.Epochs.HasLatest || s.Epochs.Latest.Seq != 2 || s.Epochs.ParkedFinishRounds != 1 {
		t.Errorf("epochs = %+v", s.Epochs)
	}
	// finished directions are not open
	if err := st.MoveDirection(bg, prof, 1, "d1", types.DirActive, "lead-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.MoveDirection(bg, prof, 1, "d1", types.DirFinished, "lead-1"); err != nil {
		t.Fatal(err)
	}
	s, _ = f.reader.Snapshot(bg, f.session)
	if len(s.Directions) != 0 {
		t.Errorf("finished direction listed as open: %+v", s.Directions)
	}
}

func TestEvidenceSeqGrowsWithNewEvidenceRowsOfThisSessionOnly(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	task := f.addTask(t, "review", 0)
	seq := func() int64 {
		s, err := f.reader.Snapshot(bg, f.session)
		if err != nil {
			t.Fatal(err)
		}
		return s.EvidenceSeq
	}
	if seq() != 0 {
		t.Fatal("no evidence yet")
	}
	other := addSession(t, f.db, "running", "mission")
	res, _ := f.db.Exec(`INSERT INTO tasks (profile_id, title, status, position, session_id, created_at, updated_at) VALUES ('default','t','ready',1,?, 'x','x')`, other)
	ot, _ := res.LastInsertId()
	mustExec(t, f.db, `INSERT INTO task_artifacts (task_id, kind, ref, at) VALUES (?, 'note', 'r', 'x')`, ot)
	if seq() != 0 {
		t.Error("another session's evidence counted")
	}
	mustExec(t, f.db, `INSERT INTO task_artifacts (task_id, kind, ref, at) VALUES (?, 'note', 'r', 'x')`, task)
	a := seq()
	mustExec(t, f.db, `INSERT INTO task_artifacts (task_id, kind, ref, at) VALUES (?, 'note', 'r2', 'x')`, task)
	if b := seq(); a <= 0 || b <= a {
		t.Errorf("seq did not grow: %d then %d", a, b)
	}
}
