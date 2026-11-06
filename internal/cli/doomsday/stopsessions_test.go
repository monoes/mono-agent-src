package doomsday

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// notes records the chat notices the stop wrote.
type notes struct {
	mu   sync.Mutex
	got  map[string][]string
	fail error
}

func (n *notes) write(_ context.Context, conv, message string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	if n.got == nil {
		n.got = map[string][]string{}
	}
	n.got[conv] = append(n.got[conv], message)
	return nil
}

// stopRig is a started run whose boss is a lead mission of conv-a, with a direction session below it, a second
// top-level lead (conv-b) that is not the boss, and a plain board session of conv-a.
type stopRig struct {
	*wireRig
	n                       *notes
	boss, kid, other, board int64
}

func newStopRig(t *testing.T) *stopRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	r := &stopRig{wireRig: w, n: &notes{}}
	w.w.ChatNote = r.n.write
	e := w.w.Engine()
	w.deps.Engine = func() Engine { return e }
	r.boss = w.topLead(t, "conv-a")
	r.other = w.topLead(t, "conv-b")
	ts := tasks.NewStore(db.DB)
	human := tasks.Actor{Kind: tasks.Human, Name: "operator"}
	kid, err := ts.CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "kid", Goal: "g", ConversationID: "conv-a", ParentSessionID: r.boss,
		Kind: tasks.SessionKindDirection, BudgetUSD: 1, MaxTasks: 1}, human)
	if err != nil {
		t.Fatal(err)
	}
	board, err := ts.CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "plain", Goal: "g", ConversationID: "conv-a",
		Kind: tasks.SessionKindBoard, BudgetUSD: 1, MaxTasks: 1}, human)
	if err != nil {
		t.Fatal(err)
	}
	r.kid, r.board = kid.ID, board.ID
	w.start(t)
	if err := w.boss(t, r.boss); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *stopRig) state(t *testing.T, id int64) tasks.SessionState {
	t.Helper()
	se, err := tasks.NewStore(r.w.DB).GetSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	return se.State
}

func (r *stopRig) states(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("boss=%s kid=%s other=%s board=%s", r.state(t, r.boss), r.state(t, r.kid), r.state(t, r.other), r.state(t, r.board))
}

// After a proven Stop the run's boss and the lead sessions of the boss's chat are stopped, and no other session is.
// Mutants tried: ending every open session of the profile (red on other), ending only the boss (red on kid), ending
// the sessions before the stop result is known (see the failure test).
func TestStopEndsOnlyTheRunsBossAndItsChatsLeadSessions(t *testing.T) {
	r := newStopRig(t)
	out, err := run(t, r.deps, "", "stop")
	if err != nil || !strings.HasPrefix(out, "stopped\n") {
		t.Fatalf("stop: %v %q", err, out)
	}
	if got := r.state(t, r.boss) + "/" + r.state(t, r.kid); got != "stopped/stopped" {
		t.Fatalf("boss/kid after stop: %s", got)
	}
	if got := r.state(t, r.other); got == tasks.SessionStopped {
		t.Fatalf("a lead of another chat was stopped: %s", r.states(t))
	}
	if got := r.state(t, r.board); got == tasks.SessionStopped {
		t.Fatalf("a plain board session was stopped: %s", r.states(t))
	}
	if !strings.Contains(out, fmt.Sprintf("sessions ended: %d, %d", r.boss, r.kid)) {
		t.Fatalf("the output does not name the ended sessions: %q", out)
	}
}

// A Stop that did not prove the stop never touches a session: not at a NOT stopped, not at a failed marker.
func TestSessionsStayWhenTheStopFails(t *testing.T) {
	r := newStopRig(t)
	before := r.states(t)
	r.w.Kernel = nil // the unit can no longer be proven
	e := r.w.Engine()
	r.deps.Engine = func() Engine { return e }
	out, err := run(t, r.deps, "", "stop")
	if err == nil || !strings.Contains(out, "NOT stopped") {
		t.Fatalf("setup: %v %q", err, out)
	}
	if after := r.states(t); after != before {
		t.Fatalf("sessions changed by a failed stop:\n before %s\n after  %s", before, after)
	}
	if len(r.n.got) != 0 {
		t.Fatalf("a transcript message was written for a failed stop: %v", r.n.got)
	}
	if strings.Contains(out, "sessions ended") || strings.Contains(out, "transcript ") {
		t.Fatalf("the failed stop reports ended sessions: %q", out)
	}
}

// The transcript message says who and when and carries the exact stop line; it goes to the chat of the ended
// sessions only, once, and the output names it for the app.
func TestStopWritesOneHonestMessageToTheRunsChat(t *testing.T) {
	r := newStopRig(t)
	out, err := run(t, r.deps, "", "stop")
	if err != nil {
		t.Fatal(err, out)
	}
	if len(r.n.got) != 1 || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("messages: %v", r.n.got)
	}
	msg := r.n.got["conv-a"][0]
	if !strings.HasPrefix(msg, "Open-ended run stopped by you at ") || !strings.HasSuffix(msg, ": stopped") {
		t.Fatalf("message: %q", msg)
	}
	if !strings.Contains(out, "\ntranscript conv-a: "+msg+"\n") {
		t.Fatalf("the output does not carry the message for the app: %q", out)
	}
}

// Whatever the verdict line is (a best-effort stop says so in it), the message carries it verbatim, never a bare
// "stopped". Calling the step twice is a no-op the second time: the sessions are already stopped and nothing is
// written again.
func TestAfterStopCarriesTheVerdictVerbatimAndIsIdempotent(t *testing.T) {
	r := newStopRig(t)
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	o := StopOutcome{MarkerRecorded: true, IntentRecorded: true, Runs: []RunRef{{CharterID: runs[0].CharterID, Generation: runs[0].Generation}}}
	verdict := "stopped (best effort: processes of the run were not proven gone)"
	lines := r.w.afterStop(context.Background(), o, verdict, time.Date(2026, 11, 6, 14, 3, 5, 0, time.UTC))
	want := "Open-ended run stopped by you at 2026-11-06 14:03:05 UTC: " + verdict
	if got := r.n.got["conv-a"]; len(got) != 1 || got[0] != want {
		t.Fatalf("message: %v, want %q (lines %v)", got, want, lines)
	}
	again := r.w.afterStop(context.Background(), o, verdict, time.Now())
	if len(again) != 0 || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("the second call was not a no-op: lines %v, messages %v", again, r.n.got)
	}
}

// A message that cannot be written does not undo the stop or the sessions, and the output says so.
func TestAFailedTranscriptWriteIsReportedAndDoesNotUndoTheStop(t *testing.T) {
	r := newStopRig(t)
	r.n.fail = errors.New("db locked")
	out, err := run(t, r.deps, "", "stop")
	if err != nil || !strings.HasPrefix(out, "stopped\n") || !strings.Contains(out, "transcript NOT written (conv-a)") {
		t.Fatalf("stop: %v %q", err, out)
	}
	if r.state(t, r.boss) != tasks.SessionStopped {
		t.Fatalf("the boss was not stopped: %s", r.states(t))
	}
}

// A second Stop has no open run: it fails as before and changes nothing.
func TestASecondStopChangesNothing(t *testing.T) {
	r := newStopRig(t)
	if out, err := run(t, r.deps, "", "stop"); err != nil {
		t.Fatal(err, out)
	}
	before := r.states(t)
	if out, err := run(t, r.deps, "", "stop"); err == nil {
		t.Fatalf("a second stop claimed success: %q", out)
	}
	if after := r.states(t); after != before || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("second stop changed things: %s -> %s, messages %v", before, after, r.n.got)
	}
}
