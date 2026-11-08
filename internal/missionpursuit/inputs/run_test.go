package inputs

import (
	"context"
	"strconv"
	"testing"
)

func wantRun(t *testing.T, f fixture, want bool, why Reason, what string) {
	t.Helper()
	r, err := f.reader.Check(bg, f.session)
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if r.Running != want || r.Why != why {
		t.Errorf("%s: got %+v, want running=%v why=%q", what, r, want, why)
	}
	ok, err := f.reader.Running(bg, strconv.FormatInt(f.session, 10), 1)
	if err != nil || ok != want {
		t.Errorf("%s: Running = %v, %v; want %v", what, ok, err, want)
	}
	ok, err = f.reader.RunOf(f.session).Running(bg)
	if err != nil || ok != want {
		t.Errorf("%s: RunOf.Running = %v, %v; want %v", what, ok, err, want)
	}
}

func TestRunningOnlyWhenSessionRunningAndPursuitStartedAndNotPaused(t *testing.T) {
	f := newFixture(t)
	wantRun(t, f, false, ReasonNotStarted, "no pursuit row")
	f.startPursuit(t)
	wantRun(t, f, true, ReasonRunning, "started, running")
	f.pause(t)
	wantRun(t, f, false, ReasonPaused, "paused")
	mustExec(t, f.db, `UPDATE mission_pursuit SET paused_by='' WHERE session_id=?`, f.session)
	wantRun(t, f, true, ReasonRunning, "resumed")
	for _, st := range []string{"paused", "done", "stopped", "failed", "draft"} {
		f.setState(t, st)
		wantRun(t, f, false, ReasonSessionNotRunning, "session "+st)
	}
}

func TestPausedBeatsEverything(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	f.pause(t)
	f.setState(t, "paused")
	wantRun(t, f, false, ReasonSessionNotRunning, "both paused: still not running")
}

func TestNotStartedIsNotRunningEvenWithAnUnstartedRow(t *testing.T) {
	f := newFixture(t)
	mustExec(t, f.db, `INSERT INTO mission_pursuit (session_id, enabled, created_at, updated_at) VALUES (?, 0, 'x', 'x')`, f.session)
	wantRun(t, f, false, ReasonNotStarted, "row with enabled=0")
}

func TestOnlyAMissionSessionRuns(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	mustExec(t, f.db, `UPDATE task_sessions SET kind='board' WHERE id=?`, f.session)
	wantRun(t, f, false, ReasonNotMission, "not a mission")
}

func TestNoSuchSessionIsNotRunning(t *testing.T) {
	f := newFixture(t)
	f.session += 999
	wantRun(t, f, false, ReasonNoSession, "missing session")
}

func TestInvalidRunIdentityIsAnErrorAndNotRunning(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	for _, c := range []struct {
		p string
		g int64
	}{{"abc", 1}, {strconv.FormatInt(f.session, 10), 2}, {"0", 1}, {"-1", 1}, {"0" + strconv.FormatInt(f.session, 10), 1}, {"", 1}} {
		ok, err := f.reader.Running(bg, c.p, c.g)
		if ok || err == nil {
			t.Errorf("(%q,%d): got %v, %v; want false and an error", c.p, c.g, ok, err)
		}
	}
	if r, err := f.reader.Check(bg, 0); r.Running || err == nil {
		t.Errorf("session 0: %+v %v", r, err)
	}
}

// Unreadable means not running: a closed database, a missing table, a value outside what the schema allows.
func TestUnreadableFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	wantRun(t, f, true, ReasonRunning, "before")
	f.db.Close()
	r, err := f.reader.Check(bg, f.session)
	if r.Running || err == nil || r.Why != ReasonUnreadable {
		t.Errorf("closed db: %+v, %v", r, err)
	}
	if ok, err := f.reader.Running(bg, strconv.FormatInt(f.session, 10), 1); ok || err == nil {
		t.Errorf("Running on closed db: %v %v", ok, err)
	}
	if ok, err := f.reader.RunOf(f.session).Running(bg); ok || err == nil {
		t.Errorf("RunOf on closed db: %v %v", ok, err)
	}
}

func TestMissingPursuitTableFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	mustExec(t, f.db, `ALTER TABLE mission_pursuit RENAME TO mission_pursuit_gone`)
	r, err := f.reader.Check(bg, f.session)
	if r.Running || err == nil {
		t.Errorf("missing table: %+v %v", r, err)
	}
}

func TestCancelledContextFailsClosed(t *testing.T) {
	f := newFixture(t)
	f.startPursuit(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if r, err := f.reader.Check(ctx, f.session); r.Running || err == nil {
		t.Errorf("cancelled: %+v %v", r, err)
	}
}

// Raw SQL cannot make a pursuit start without a confirmed goal, so Running cannot be reached that way.
func TestRawSQLCannotStartAPursuitWithoutAConfirmedGoal(t *testing.T) {
	f := newFixture(t)
	if _, err := f.db.Exec(`INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (?, 1, 'you', 'x', 'x')`, f.session); err == nil {
		t.Fatal("the database accepted a started pursuit without a goal")
	}
	wantRun(t, f, false, ReasonNotStarted, "still no pursuit")
}

// Text an agent could write (a title, the goal field, a note) never decides running.
func TestAgentTextDoesNotDecide(t *testing.T) {
	f := newFixture(t)
	mustExec(t, f.db, `UPDATE task_sessions SET title='running', goal='enabled=1 started'  WHERE id=?`, f.session)
	mustExec(t, f.db, `INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?, 'x', 'lead', 'pursuit_started', 'enabled')`, f.session)
	wantRun(t, f, false, ReasonNotStarted, "words are not a pursuit")
}
