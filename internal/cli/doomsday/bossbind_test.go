package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/tasks"
)

// topLead creates a top-level lead mission for profile p1 (conversation conv).
func (w *wireRig) topLead(t *testing.T, conv string) int64 {
	t.Helper()
	if _, err := w.w.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p1', 'P1')`); err != nil {
		t.Fatal(err)
	}
	se, err := tasks.NewStore(w.w.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "lead " + conv, Goal: "g", ConversationID: conv,
		Kind: tasks.SessionKindMission, BudgetUSD: 10, MaxTasks: 5}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	return se.ID
}

// boss records session as the boss of the open run, as `start --boss-session` does.
func (w *wireRig) boss(t *testing.T, session int64) error {
	t.Helper()
	runs, err := w.w.Store.OpenRuns(context.Background(), "p1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("no open run: %v %v", runs, err)
	}
	return w.w.designateBoss(context.Background(), session, Entry{CharterID: runs[0].CharterID, Generation: runs[0].Generation})
}

// Two lead missions in one profile: only the one recorded as the run's boss can begin the finish, whatever the other
// presents. Mutant tried: Wiring.Ops without Bosses (nil source) refuses the boss too (red on the first success); a
// BossOf that ignores the recorded stamp and takes the lowest top-level lead lets the second lead through when it is the
// older one (red where the recorded boss is the newer mission).
func TestOnlyTheRecordedBossOfTheRunBeginsTheFinish(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	ctx := context.Background()
	older := r.topLead(t, "conv-older")
	newer := r.topLead(t, "conv-newer")
	r.start(t)

	// no boss recorded yet: nobody
	for _, s := range []int64{older, newer} {
		if _, err := r.w.BossFinishBegin(ctx, s); !errors.Is(err, ops.ErrCallerMismatch) {
			t.Fatalf("no boss recorded, session %d: %v", s, err)
		}
	}
	if err := r.boss(t, newer); err != nil {
		t.Fatal(err)
	}
	// a second boss for the same run is refused, and so is a child session
	if err := r.boss(t, older); err == nil {
		t.Fatal("a run got a second boss")
	}
	if _, err := r.w.BossFinishBegin(ctx, older); !errors.Is(err, ops.ErrCallerMismatch) {
		t.Fatalf("another lead of the profile: %v", err)
	}
	if runs, _ := r.w.Store.OpenRuns(ctx, "p1"); len(runs) != 1 || runs[0].State != "running" {
		t.Fatalf("the refused lead changed the run: %+v", runs)
	}
	res, err := r.w.BossFinishBegin(ctx, newer)
	if err != nil || !res.Accepted {
		t.Fatalf("the boss: %+v %v", res, err)
	}
}

// A session that is not an open top-level lead mission cannot be named boss.
func TestOnlyATopLevelLeadMissionCanBeNamedBoss(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	top := r.topLead(t, "conv-top")
	kid, err := tasks.NewStore(r.w.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "kid", Goal: "g", ParentSessionID: top,
		Kind: tasks.SessionKindDirection, BudgetUSD: 1, MaxTasks: 1}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	r.start(t)
	for _, s := range []int64{kid.ID, 9999} {
		if err := r.boss(t, s); err == nil {
			t.Fatalf("session %d was named boss", s)
		}
	}
	if err := r.boss(t, top); err != nil {
		t.Fatal(err)
	}
}

// The plan, ask and grant tools of the other lead are refused the same way (the route builds caller == addressed).
func TestAnotherLeadCannotAnswerAnAskOfTheRun(t *testing.T) {
	r := newOpsRig(t)
	r.w.InstallOps(r.host)
	ctx := context.Background()
	rc, pc, oc, cc := r.chat(t), r.chat(t), r.chat(t), r.chat(t)
	R := r.session(t, 0, rc, 100, 10)
	P := r.session(t, R.ID, pc, 50, 10)
	r.session(t, 0, oc, 100, 10) // a second lead mission of the profile
	r.session(t, P.ID, cc, 20, 10)
	r.start(t)
	asked, err := r.host.AskAncestor(lead(cc), "p1", coderhost.AskRequest{Question: "q", Importance: coderhost.AskCritical})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); err == nil || !strings.Contains(err.Error(), "boss") {
		t.Fatalf("a lead before any boss was recorded: %v", err)
	}
	if err := r.boss(t, R.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.host.BossAnswerAsk(ctx, lead(oc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); err == nil {
		t.Fatal("another lead answered")
	}
	if a, _, err := r.host.BossAnswerAsk(ctx, lead(rc), "p1", asked.AskID, coderhost.ClassAskCritical, "yes"); err != nil || a.Status != "answered" {
		t.Fatalf("the boss: %+v %v", a, err)
	}
}

// finish.begin refused over and over by the preconditions writes a bounded number of denied-attempt rows, counted from the
// stream itself (a boss call may run in a fresh process). Mutant tried: removing the count check in FinishBegin's deny
// makes every call write a row (red).
func TestDeniedFinishAttemptsAreBounded(t *testing.T) {
	r := newFinRig(t, strings.Replace(finishCharterJSON, `"criteria":[{"kind":"command","command":"echo hello","expected":"hello"},{"kind":"rubric","text":"the docs read well"}],`, "", 1))
	top := r.topLead(t, "conv-boss")
	r.start(t)
	if err := r.boss(t, top); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < ops.MaxParkRows+20; i++ {
		res, err := r.w.BossFinishBegin(ctx, top)
		if err != nil || res.Accepted || res.Refused == "" {
			t.Fatalf("call %d: %+v %v", i, res, err)
		}
	}
	if n := count2(t, r, `SELECT COUNT(*) FROM doomsday_audit WHERE kind = 'denied-attempt'`); n != ops.MaxParkRows {
		t.Fatalf("denied-attempt rows = %d, want %d", n, ops.MaxParkRows)
	}
}

// BossOf reads only a stamped, top-level, current-generation lead. Mutants tried: dropping the top-level condition
// (COALESCE(parent_session_id, 0) = 0) returns a stamped child (red on the child step); dropping the generation
// condition returns a boss stamped for another generation (red on the generation step).
func TestBossOfRequiresTopLevelAndTheCurrentGeneration(t *testing.T) {
	r := newFinRig(t, finishCharterJSON)
	ctx := context.Background()
	top := r.topLead(t, "conv-top")
	other := r.topLead(t, "conv-other")
	r.start(t)
	runs, err := r.w.Store.OpenRuns(ctx, "p1")
	if err != nil || len(runs) != 1 {
		t.Fatalf("no open run: %v %v", runs, err)
	}
	act := ops.Active{ID: runs[0].CharterID, Generation: runs[0].Generation, Profile: "p1"}
	src := BossSource{Wiring: r.w}
	if err := r.boss(t, top); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := src.BossOf(ctx, act); err != nil || !ok || id != top {
		t.Fatalf("the recorded boss: %d %v %v", id, ok, err)
	}
	// another generation of the same charter has no boss here
	wrong := act
	wrong.Generation++
	if _, ok, err := src.BossOf(ctx, wrong); err != nil || ok {
		t.Fatalf("a boss stamped for generation %d answered for %d: %v %v", act.Generation, wrong.Generation, ok, err)
	}
	// a stamped row that is not top-level is not the boss: move the stamp to a child
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET doomsday_charter_id = '', doomsday_generation = 0 WHERE id = ?`, top); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET parent_session_id = ?, doomsday_charter_id = ?, doomsday_generation = ? WHERE id = ?`,
		top, act.ID, act.Generation, other); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := src.BossOf(ctx, act); err != nil || ok {
		t.Fatalf("a stamped child was taken for the boss: %d %v %v", id, ok, err)
	}
}
