package doomsday

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/monoes/mono-agent/internal/coderhost"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
	"github.com/monoes/mono-agent/internal/tasks"
)

// reopen puts a session back into running, as if the stop's transition had not reached it.
func (r *stopRig) reopen(t *testing.T, id int64) {
	t.Helper()
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

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

func (n *notes) exists(_ context.Context, _ RowQuerier, conv string, _ int64) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.got[conv]) > 0, nil
}

func newStopRig(t *testing.T) *stopRig {
	t.Helper()
	w, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	w.w.DB = db.DB
	r := &stopRig{wireRig: w, n: &notes{}}
	w.w.ChatNote = r.n.write
	w.w.ChatNoteSince = r.n.exists
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
	lines := r.w.afterStop(context.Background(), o, verdict, time.Date(2026, 11, 6, 14, 3, 5, 0, time.UTC)).Lines
	want := "Open-ended run stopped by you at 2026-11-06 14:03:05 UTC: " + verdict
	if got := r.n.got["conv-a"]; len(got) != 1 || got[0] != want {
		t.Fatalf("message: %v, want %q (lines %v)", got, want, lines)
	}
	again := r.w.afterStop(context.Background(), o, verdict, time.Now()).Lines
	if len(again) != 0 || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("the second call was not a no-op: lines %v, messages %v", again, r.n.got)
	}
}

// A message that cannot be written does not undo the stop or the sessions, and the output says so.
func TestAFailedTranscriptWriteIsReportedAndDoesNotUndoTheStop(t *testing.T) {
	r := newStopRig(t)
	r.n.fail = errors.New("db locked")
	out, err := run(t, r.deps, "", "stop")
	if clicore.ExitCodeFor(err) != ExitCleanupFailed || !strings.HasPrefix(out, "stopped\n") || !strings.Contains(out, "transcript NOT written (conv-a)") {
		t.Fatalf("stop: %v (exit %d) %q", err, clicore.ExitCodeFor(err), out)
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

// The transition failed after a proven stop (a session left open): the next stop finds no open run, finishes the
// sessions, says so, exits 0 and does not write the message a second time.
func TestASecondStopFinishesWhatTheFirstLeftOpen(t *testing.T) {
	r := newStopRig(t)
	if out, err := run(t, r.deps, "", "stop"); err != nil {
		t.Fatal(err, out)
	}
	r.reopen(t, r.boss)
	r.reopen(t, r.kid)
	out, err := run(t, r.deps, "", "stop")
	want := fmt.Sprintf("no open run; ended sessions left open by the previous stop: %d, %d\n", r.boss, r.kid)
	if err != nil || !strings.HasPrefix(out, want) {
		t.Fatalf("repair: %v %q", err, out)
	}
	if got := r.state(t, r.boss) + "/" + r.state(t, r.kid); got != "stopped/stopped" {
		t.Fatalf("not repaired: %s", got)
	}
	if len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("the message was written again: %v", r.n.got)
	}
	// and a third stop has nothing left: the old failure
	if out, err := run(t, r.deps, "", "stop"); err == nil || strings.Contains(out, "ended sessions") {
		t.Fatalf("third stop: %v %q", err, out)
	}
}

// stopWithoutCleanup stops the run through the Stop order alone, as a stop whose transition never ran: the run is quiescent
// by an operator stop, every session is still open and no message exists.
func (r *stopRig) stopWithoutCleanup(t *testing.T) {
	t.Helper()
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	rep := r.w.Stopper("operator", r.w.ender()).Stop(context.Background(), uint64(runs[0].Generation), runs[0].CharterID, operatorStopCause)
	if !rep.Stopped() {
		t.Fatalf("setup: %v", rep.Lines())
	}
}

// TRUST: a repair that ends no session in a chat writes nothing there. The run's sessions were all ended by hand, so the
// repair has nothing to do and the old failure stands; no message claims a stop.
func TestARepairThatEndsNothingWritesNoMessage(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	ts := tasks.NewStore(r.w.DB)
	for _, id := range []int64{r.kid, r.boss} {
		if _, err := ts.StopSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, r.deps, "", "stop")
	if err == nil || strings.Contains(out, "ended sessions") || len(r.n.got) != 0 {
		t.Fatalf("a repair with nothing to end: %v %q notes=%v", err, out, r.n.got)
	}
}

// TRUST: a repair that ends sessions writes one message that says exactly that: the run was stopped earlier, its sessions
// were ended by a later stop. It never says "stopped by you at" (nothing was stopped now), and it is written once.
func TestARepairThatEndsSessionsWritesAMessageThatSaysSo(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	out, err := run(t, r.deps, "", "stop")
	if err != nil || !strings.HasPrefix(out, fmt.Sprintf("no open run; ended sessions left open by the previous stop: %d, %d\n", r.boss, r.kid)) {
		t.Fatalf("repair: %v %q", err, out)
	}
	got := r.n.got["conv-a"]
	if len(got) != 1 || !strings.HasPrefix(got[0], "Open-ended run stopped earlier; its sessions were ended by a later stop at ") || strings.Contains(got[0], "stopped by you") {
		t.Fatalf("message: %v", got)
	}
	if len(r.n.got) != 1 {
		t.Fatalf("a message went to a chat other than the ended one: %v", r.n.got)
	}
	if out, err := run(t, r.deps, "", "stop"); err == nil || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("third stop: %v %q %v", err, out, r.n.got)
	}
}

// TRUST: the first stop's message never claims a stop that failed. A run that did not stop gets none; a run that stopped while
// another did not says so in the same line.
func TestTheFirstStopsMessageNeverClaimsAStopThatFailed(t *testing.T) {
	r := newStopRig(t)
	r.w.Kernel = nil
	e := r.w.Engine()
	r.deps.Engine = func() Engine { return e }
	if out, err := run(t, r.deps, "", "stop"); err == nil || len(r.n.got) != 0 {
		t.Fatalf("failed stop: %v %q notes=%v", err, out, r.n.got)
	}
	r2 := newStopRig(t)
	runs, _ := r2.w.Store.OpenRuns(context.Background(), "p1")
	o := StopOutcome{Incomplete: []string{"run 2 not stopped"}, Runs: []RunRef{{CharterID: runs[0].CharterID, Generation: runs[0].Generation}}}
	a := &realAfter{w: r2.wireRig}
	r2.deps.Engine = func() Engine { return &partialEngine{fakeEngine: &fakeEngine{stop: o}, realAfter: a} }
	if _, err := run(t, r2.deps, "", "stop"); err == nil {
		t.Fatal("a partial stop exited 0")
	}
	got := r2.n.got["conv-a"]
	if len(got) != 1 || !strings.Contains(got[0], "another run of this profile was NOT stopped") {
		t.Fatalf("message of a partial stop: %v", got)
	}
}

type realAfter struct{ w *wireRig }
type partialEngine struct {
	*fakeEngine
	*realAfter
}

func (p *partialEngine) AfterStop(ctx context.Context, o StopOutcome, verdict string, at time.Time) AfterReport {
	return p.w.w.afterStop(ctx, o, verdict, at)
}

// A run that finished by itself (a finish ends the run quiescent with the finish cause) is not "the last stop": a later bare
// stop ends none of its sessions, writes no message and fails as before. Mutant tried: dropping the operator-stop cause check.
func TestALaterStopNeverEndsTheSessionsOfAFinishEndedRun(t *testing.T) {
	r := newStopRig(t)
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	if rep := r.w.Stopper("operator", r.w.ender()).Stop(context.Background(), uint64(runs[0].Generation), runs[0].CharterID, store.FinishCause); !rep.Stopped() {
		t.Fatalf("setup: %v", rep.Lines())
	}
	before := r.states(t)
	out, err := run(t, r.deps, "", "stop")
	if err == nil || strings.Contains(out, "ended sessions") || r.states(t) != before || len(r.n.got) != 0 {
		t.Fatalf("a finish-ended run was repaired: %v %q %s notes=%v", err, out, r.states(t), r.n.got)
	}
}

// A run stopped more than a week ago is not repaired: a hint, the old failure, nothing touched.
func TestARunStoppedLongAgoIsLeftAlone(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	before := r.states(t)
	lines, st := r.w.repairStop(context.Background(), time.Now().Add(8*24*time.Hour))
	if st != RepairNothing || len(lines) != 1 || !strings.Contains(lines[0], "more than 7 days ago") || r.states(t) != before || len(r.n.got) != 0 {
		t.Fatalf("old run: %v %v %s", lines, st, r.states(t))
	}
	if _, st := r.w.repairStop(context.Background(), time.Now()); st != RepairDone {
		t.Fatalf("a fresh run was not repaired: %v", st)
	}
}

// The exact edge of the window: seven days to the second is still repaired, one second more is not.
func TestTheRepairWindowIsExactlySevenDays(t *testing.T) {
	for _, c := range []struct {
		extra time.Duration
		want  RepairStatus
	}{{0, RepairDone}, {time.Second, RepairNothing}} {
		r := newStopRig(t)
		r.stopWithoutCleanup(t)
		run, _ := r.w.Store.GetRun(context.Background(), "p1", 1)
		_, st := r.w.repairStop(context.Background(), time.Unix(run.IntentAt, 0).Add(repairWindow+c.extra))
		if st != c.want {
			t.Fatalf("at the window + %v: %v, want %v", c.extra, st, c.want)
		}
	}
}

// The ">7 days" hint is printed only when something is actually left open; otherwise a bare stop says nothing extra.
func TestTheOldRunHintIsPrintedOnlyWhenSessionsAreOpen(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	old := time.Now().Add(9 * 24 * time.Hour)
	if lines, _ := r.w.repairStop(context.Background(), old); len(lines) != 1 {
		t.Fatalf("sessions open, no hint: %v", lines)
	}
	ts := tasks.NewStore(r.w.DB)
	for _, id := range []int64{r.kid, r.boss} {
		if _, err := ts.StopSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	if lines, st := r.w.repairStop(context.Background(), old); len(lines) != 0 || st != RepairNothing {
		t.Fatalf("nothing open, yet: %v %v", lines, st)
	}
}

// owedFlag is conv-a's owed-message flag (nil when none).
func (r *stopRig) owedFlag(t *testing.T, conv string) *coderhost.OwedStopNotice {
	t.Helper()
	st, err := r.w.coder().GetSettings("p1", conv)
	if err != nil {
		t.Fatal(err)
	}
	return st.OwedStopNotice
}

// setOwed stores o as conv's flag through the one settings writer, as a stop does.
func (r *stopRig) setOwed(t *testing.T, conv string, o coderhost.OwedStopNotice) error {
	t.Helper()
	_, err := r.w.coder().UpdateSettings("p1", conv, func(_ context.Context, _ *sql.Conn, cur coderhost.Settings) (coderhost.Settings, error) {
		cur.OwedStopNotice = &o
		return cur, nil
	})
	return err
}

// DEAD END FIX: the first stop ends every session but cannot write the message. It records the message as owed before it
// reports exit 8; the next stop writes exactly that message, clears the record, exits 0; the one after is the plain old failure.
func TestAMessageTheFirstStopCouldNotWriteIsWrittenByTheNextStop(t *testing.T) {
	r := newStopRig(t)
	r.n.fail = errors.New("db locked")
	out, err := run(t, r.deps, "", "stop")
	if clicore.ExitCodeFor(err) != ExitCleanupFailed || !strings.Contains(out, "transcript NOT written (conv-a)") {
		t.Fatalf("first: %v %q", err, out)
	}
	if f := r.owedFlag(t, "conv-a"); f == nil || f.Code != owedPlain {
		t.Fatalf("the owed message was not recorded before the failure was reported: %+v", f)
	}
	if r.state(t, r.boss) != tasks.SessionStopped {
		t.Fatal("setup: the sessions should be ended")
	}
	r.n.fail = nil
	out, err = run(t, r.deps, "", "stop")
	got := r.n.got["conv-a"]
	if err != nil || !strings.HasPrefix(out, repairHeadNote+"\n") || len(got) != 1 || !strings.HasPrefix(got[0], "Open-ended run stopped by you at ") || !strings.HasSuffix(got[0], ": stopped") {
		t.Fatalf("second: %v %q notes=%v", err, out, r.n.got)
	}
	if f := r.owedFlag(t, "conv-a"); f != nil {
		t.Fatalf("the flag is still there: %+v", f)
	}
	if out, err = run(t, r.deps, "", "stop"); err == nil || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("third: %v %q %v", err, out, r.n.got)
	}
}

// A record that cannot be honoured is not: another run's (charter, generation or stop time differ), or one past the window.
// A chat whose sessions were ended by hand and that has no record gets nothing.
func TestAnOwedRecordOfAnotherRunOrAnExpiredOneIsNotHonoured(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	ts := tasks.NewStore(r.w.DB)
	for _, id := range []int64{r.kid, r.boss} {
		if _, err := ts.StopSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	run1, _ := r.w.Store.GetRun(context.Background(), "p1", 1)
	ref := RunRef{CharterID: run1.CharterID, Generation: 1}
	for name, o := range map[string]coderhost.OwedStopNotice{
		"another charter": {Charter: "other", Generation: 1, IntentAt: run1.IntentAt, Code: owedPlain, At: run1.IntentAt},
		"another stop":    {Charter: ref.CharterID, Generation: 1, IntentAt: run1.IntentAt + 5, Code: owedPlain, At: run1.IntentAt},
		"another gen":     {Charter: ref.CharterID, Generation: 2, IntentAt: run1.IntentAt, Code: owedPlain, At: run1.IntentAt},
		"a time outside":  {Charter: ref.CharterID, Generation: 1, IntentAt: run1.IntentAt, Code: owedPlain, At: 1},
	} {
		if err := r.setOwed(t, "conv-a", o); err != nil {
			t.Fatal(err)
		}
		if lines, st := r.w.repairStop(context.Background(), time.Now()); st != RepairNothing || len(lines) != 0 || len(r.n.got) != 0 {
			t.Fatalf("%s: honoured: %v %v %v", name, lines, st, r.n.got)
		}
	}
	// a good flag past the window
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	if lines, st := r.w.repairStop(context.Background(), time.Unix(run1.IntentAt, 0).Add(repairWindow+time.Second)); st != RepairNothing || len(lines) != 0 || len(r.n.got) != 0 {
		t.Fatalf("expired: honoured: %v %v %v", lines, st, r.n.got)
	}
	// the expired record was dropped when seen; the same record inside the window is honoured: the test is not vacuous
	if f := r.owedFlag(t, "conv-a"); f != nil {
		t.Fatalf("an expired flag was kept: %+v", f)
	}
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	if _, st := r.w.repairStop(context.Background(), time.Now()); st != RepairDone || len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("a good record: %v %v", st, r.n.got)
	}
}

// A chat whose note check cannot be read gets no message, and the output says so (it is not silently taken for "has one").
func TestAnUnreadableNoteCheckIsReported(t *testing.T) {
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	r.w.ChatNoteSince = func(context.Context, RowQuerier, string, int64) (bool, error) { return false, errors.New("boom") }
	lines, st := r.w.repairStop(context.Background(), time.Now())
	if st != RepairFailed || !strings.Contains(strings.Join(lines, "\n"), "transcript check failed (conv-a): boom") || len(r.n.got) != 0 {
		t.Fatalf("%v %v %v", lines, st, r.n.got)
	}
}

// The strings the app matches on are the ones the Go side prints: one fixture, read by both.
func TestTheRepairHeadsMatchTheFrontendFixture(t *testing.T) {
	b, err := os.ReadFile("../../../wails-app/frontend/src/components/chat/stopOutput.fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]string
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f["repairEnded"] != repairHeadEnded || f["repairNote"] != repairHeadNote || f["repairFailed"] != repairHeadFailed || f["repairNoteFailed"] != repairHeadNoteFailed {
		t.Fatalf("the Go heads and the frontend fixture differ: %v", f)
	}
}

// If #1503 adds StoppedBestEffort to stop.Report it must be a value-receiver func() bool, or runEnded would miss it: a renamed
// method, a pointer receiver or another signature fails here instead of silently ending no sessions.
func TestAnyBestEffortMethodOfStopReportIsTheOneRunEndedReads(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(stop.Report{}), reflect.TypeOf(&stop.Report{})} {
		for i := 0; i < typ.NumMethod(); i++ {
			m := typ.Method(i)
			if !strings.Contains(m.Name, "BestEffort") {
				continue
			}
			if m.Name != "StoppedBestEffort" || typ.Kind() == reflect.Ptr && !reflect.TypeOf(stop.Report{}).Implements(reflect.TypeOf((*interface{ StoppedBestEffort() bool })(nil)).Elem()) ||
				m.Type.NumIn() != 1 || m.Type.NumOut() != 1 || m.Type.Out(0).Kind() != reflect.Bool {
				t.Fatalf("stop.Report.%s has a shape runEnded does not read (%v): update runEnded", m.Name, m.Type)
			}
		}
	}
}

type fakeBE struct{ stopped, best bool }

func (f fakeBE) Stopped() bool           { return f.stopped }
func (f fakeBE) StoppedBestEffort() bool { return f.best }

// A report that is best-effort stopped counts as an ended run, through the optional method #1503 adds to stop.Report.
func TestRunEndedHonoursABestEffortReport(t *testing.T) {
	if !runEnded(fakeBE{best: true}) || !runEnded(fakeBE{stopped: true}) || runEnded(fakeBE{}) {
		t.Fatal("runEnded")
	}
	if runEnded(stop.Report{}) {
		t.Fatal("an empty report counts as ended")
	}
}

// Enabled by merging #1503: the real Report then has StoppedBestEffort, and runEnded must read it with no edit.
func TestRunEndedReadsTheRealBestEffortReport(t *testing.T) {
	if _, ok := any(stop.Report{}).(interface{ StoppedBestEffort() bool }); !ok {
		t.Skip("needs #1503: stop.Report has no StoppedBestEffort yet")
	}
	var _ stoppedReport = stop.Report{}
	// With #1503, build a report whose unit ended UnitBestEffort (see its stop tests) and assert runEnded(rep) is true while
	// rep.Stopped() is false; the merge note in the PR text names the helper to copy.
}

// The CLI side of a best-effort stop: an outcome that carries the engine's line and names its run ends the sessions, prints
// the line first and writes it verbatim into the chat message.
func TestABestEffortLineEndsSessionsAndReachesTheMessageVerbatim(t *testing.T) {
	r := newStopRig(t)
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	line := "stopped (best effort: processes that left the group are not covered)"
	o := StopOutcome{MarkerRecorded: true, IntentRecorded: true, Line: line, Runs: []RunRef{{CharterID: runs[0].CharterID, Generation: runs[0].Generation}}}
	r.deps.Engine = func() Engine {
		return &partialEngine{fakeEngine: &fakeEngine{stop: o}, realAfter: &realAfter{w: r.wireRig}}
	}
	out, err := run(t, r.deps, "", "stop")
	got := r.n.got["conv-a"]
	if err != nil || !strings.HasPrefix(out, line+"\n") || len(got) != 1 || !strings.HasSuffix(got[0], ": "+line) || r.state(t, r.boss) != tasks.SessionStopped {
		t.Fatalf("best-effort: %v %q notes=%v %s", err, out, got, r.states(t))
	}
}

// The repair belongs to the last run's boss and its chat as the stop left it: not another chat's lead, a mission the person
// started in the chat after the stop, or another profile's session.
func TestRepairNeverTouchesSessionsOfAnotherRunProfileOrLaterMission(t *testing.T) {
	r := newStopRig(t)
	if out, err := run(t, r.deps, "", "stop"); err != nil {
		t.Fatal(err, out)
	}
	if _, err := r.w.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('p2', 'P2')`); err != nil {
		t.Fatal(err)
	}
	foreign, err := tasks.NewStore(r.w.DB).CreateSession(context.Background(), "p2", tasks.SessionInput{Title: "x", Goal: "g", ConversationID: "conv-a",
		Kind: tasks.SessionKindMission, BudgetUSD: 1, MaxTasks: 1}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	later, err := tasks.NewStore(r.w.DB).CreateSession(context.Background(), "p1", tasks.SessionInput{Title: "later", Goal: "g", ConversationID: "conv-a",
		Kind: tasks.SessionKindMission, BudgetUSD: 1, MaxTasks: 1}, tasks.Actor{Kind: tasks.Human, Name: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET created_at = '2999-01-01T00:00:00Z' WHERE id = ?`, later.ID); err != nil {
		t.Fatal(err)
	}
	r.reopen(t, r.kid) // something to repair, so the repair really runs
	out, err := run(t, r.deps, "", "stop")
	if err != nil || !strings.Contains(out, fmt.Sprintf("left open by the previous stop: %d\n", r.kid)) {
		t.Fatalf("repair: %v %q", err, out)
	}
	for name, id := range map[string]int64{"another chat's lead": r.other, "board": r.board} {
		if r.state(t, id) == tasks.SessionStopped {
			t.Fatalf("%s was stopped by the repair: %s", name, r.states(t))
		}
	}
	if got := stateOf(t, r, "p1", later.ID); got == tasks.SessionStopped {
		t.Fatal("a mission started after the stop was stopped by the repair")
	}
	if got := stateOf(t, r, "p2", foreign.ID); got == tasks.SessionStopped {
		t.Fatal("another profile's session was stopped by the repair")
	}
}

func stateOf(t *testing.T, r *stopRig, profile string, id int64) tasks.SessionState {
	t.Helper()
	se, err := tasks.NewStore(r.w.DB).GetSession(context.Background(), profile, id, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		t.Fatal(err)
	}
	return se.State
}

// afterEngine is the fake engine with an AfterStop that records what it was handed.
type afterEngine struct {
	*fakeEngine
	calls    []string
	runsSeen [][]RunRef
}

func (a *afterEngine) AfterStop(_ context.Context, o StopOutcome, verdict string, _ time.Time) AfterReport {
	a.calls = append(a.calls, verdict)
	a.runsSeen = append(a.runsSeen, o.Runs)
	return AfterReport{}
}

// The seam for a best-effort stop (PR #1503, which prints "stopped (best effort: ...)" and exits 0): the sessions step runs
// for any exit-0 stop whose outcome names runs, and receives the line the command printed, so a best-effort verdict reaches
// the message verbatim. A stop that failed with no run proven stopped never calls it. When #1503 merges, its BestEffort
// branch must keep both properties.
func TestAfterStopRunsForWhateverRunsTheOutcomeNames(t *testing.T) {
	r := newRig()
	a := &afterEngine{fakeEngine: r.e}
	r.deps.Engine = func() Engine { return a }
	ran := []RunRef{{CharterID: "c", Generation: 1}}
	r.e.stop = StopOutcome{MarkerRecorded: true, IntentRecorded: true, Runs: ran}
	out, err := run(t, r.deps, "", "stop")
	if err != nil || len(a.calls) != 1 || a.calls[0] != "stopped" || out != "stopped\n" {
		t.Fatalf("exit-0 stop: %v calls=%v out=%q", err, a.calls, out)
	}
	for _, bad := range []StopOutcome{
		{MarkerRecorded: true, IntentRecorded: true, Survivors: []string{"pid 7"}},
		{MarkerRecorded: false, IntentRecorded: true},
		{MarkerRecorded: true, IntentRecorded: true, Incomplete: []string{"x"}},
	} {
		r.e.stop = bad
		if _, err := run(t, r.deps, "", "stop"); err == nil {
			t.Fatal("a failed outcome exited 0")
		}
	}
	if len(a.calls) != 1 {
		t.Fatalf("the sessions step ran for a stop that proved no run: %v", a.calls)
	}
}

// Two runs, one stopped and one not: the error stays, and the stopped run's sessions are still ended.
func TestAStoppedRunIsEndedEvenWhenAnotherRunDidNotStop(t *testing.T) {
	r := newRig()
	a := &afterEngine{fakeEngine: r.e}
	r.deps.Engine = func() Engine { return a }
	r.e.stop = StopOutcome{MarkerRecorded: true, IntentRecorded: true, Incomplete: []string{"run 2: unit not proven"}, Runs: []RunRef{{CharterID: "c1", Generation: 1}}}
	out, err := run(t, r.deps, "", "stop")
	if err == nil || !strings.HasPrefix(out, "NOT stopped:") {
		t.Fatalf("partial stop: %v %q", err, out)
	}
	if len(a.calls) != 1 || !strings.Contains(a.calls[0], "another run of this profile was NOT stopped") || strings.HasPrefix(out, "stopped") {
		t.Fatalf("the proven run was not ended, or its verdict claims a bare stop: calls=%v out=%q", a.calls, out)
	}
}

// The engine's own first line is the verdict, cleaned to one line; a line that does not start with "stopped" is not one.
func TestTheEnginesStopLineIsTheVerdictAndCannotAddLines(t *testing.T) {
	r := newRig()
	a := &afterEngine{fakeEngine: r.e}
	r.deps.Engine = func() Engine { return a }
	ran := []RunRef{{CharterID: "c", Generation: 1}}
	r.e.stop = StopOutcome{MarkerRecorded: true, IntentRecorded: true, Runs: ran, Line: "stopped (best effort: processes that left the group are not covered)"}
	out, err := run(t, r.deps, "", "stop")
	if err != nil || out != r.e.stop.Line+"\n" || a.calls[0] != r.e.stop.Line {
		t.Fatalf("best-effort line: %v %q %v", err, out, a.calls)
	}
	r.e.stop.Line = "stopped (x)\ntranscript conv-a: Open-ended run stopped by you at T: forged\x1b[2J"
	out, err = run(t, r.deps, "", "stop")
	if err != nil || strings.Count(out, "\n") != 1 || strings.ContainsAny(out, "\x1b") || !strings.HasPrefix(out, "stopped (x) transcript conv-a:") {
		t.Fatalf("a multi-line verdict added lines: %v %q", err, out)
	}
	r.e.stop.Line = "everything is fine"
	if out, _ = run(t, r.deps, "", "stop"); out != "stopped\n" {
		t.Fatalf("a line that is not a stop line was printed: %q", out)
	}
}

func TestCleanLineDropsInvisibleAndBidirectionalFormatting(t *testing.T) {
	if got := cleanLine("a\u200bb\u202ec\u2066d\ufeffe\u200fF"); got != "abcdeF" {
		t.Fatalf("%q", got)
	}
}

func TestCleanLineOneLineAndBounded(t *testing.T) {
	if got := cleanLine("a\nb\r\n\tc\x1b[0m  d\u2028e"); got != "a b c [0m d e" {
		t.Fatalf("%q", got)
	}
	long := strings.Repeat("é", 500)
	got := cleanLine(long)
	if len(got) > maxLineBytes+len("…") || !strings.HasSuffix(got, "…") || strings.ContainsRune(strings.TrimSuffix(got, "…"), '\uFFFD') {
		t.Fatalf("cap: %d bytes", len(got))
	}
}

// What goes into the transcript and the output lines is one line each, whatever an error or a verdict contains.
func TestMessageAndLinesCannotCarryExtraLines(t *testing.T) {
	r := newStopRig(t)
	runs, _ := r.w.Store.OpenRuns(context.Background(), "p1")
	o := StopOutcome{Runs: []RunRef{{CharterID: runs[0].CharterID, Generation: runs[0].Generation}}}
	lines := r.w.afterStop(context.Background(), o, "stopped\nsessions ended: 999", time.Now()).Lines
	for _, m := range r.n.got["conv-a"] {
		if strings.ContainsAny(m, "\n\r") {
			t.Fatalf("message with a line break: %q", m)
		}
	}
	for _, l := range lines {
		if strings.ContainsAny(l, "\n\r") {
			t.Fatalf("line with a line break: %q", l)
		}
	}
}

// One session that cannot be stopped: the verdict stays the first line, the line names the session, the others are still
// ended, and the exit code (8) says the cleanup is not complete; a later stop finishes it and exits 0.
func TestASessionThatCannotBeEndedIsNamedAndTheExitCodeSaysSo(t *testing.T) {
	r := newStopRig(t)
	if _, err := r.w.DB.Exec(fmt.Sprintf(`CREATE TRIGGER t5_boom BEFORE UPDATE OF state ON task_sessions WHEN NEW.id = %d BEGIN SELECT RAISE(ABORT, 'boom'); END`, r.kid)); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r.deps, "", "stop")
	if clicore.ExitCodeFor(err) != ExitCleanupFailed || !strings.HasPrefix(out, "stopped\n") || !strings.Contains(out, fmt.Sprintf("session %d NOT ended: ", r.kid)) {
		t.Fatalf("stop: %v (exit %d) %q", err, clicore.ExitCodeFor(err), out)
	}
	if r.state(t, r.boss) != tasks.SessionStopped || r.state(t, r.kid) == tasks.SessionStopped {
		t.Fatalf("the others must still end: %s", r.states(t))
	}
	if _, err := r.w.DB.Exec(`DROP TRIGGER t5_boom`); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, r.deps, "", "stop"); err != nil || !strings.Contains(out, fmt.Sprintf("left open by the previous stop: %d", r.kid)) {
		t.Fatalf("repair: %v %q", err, out)
	}
}

// A session that left the open states between the read and the stop is not a failure and prints nothing.
func TestASessionAlreadyOutOfTheOpenStatesIsNotReportedAsNotEnded(t *testing.T) {
	r := newStopRig(t)
	if out, err := run(t, r.deps, "", "stop"); err != nil {
		t.Fatal(err, out)
	}
	lines, ended, _ := r.w.endSessions(context.Background(), []runSession{{id: r.boss, conv: "conv-a"}})
	if len(lines) != 0 || len(ended) != 0 {
		t.Fatalf("lines %v ended %v", lines, ended)
	}
}

// The filters that keep the step inside the run: an archived lead, another generation's boss and an archived boss's chat are
// never touched. Mutants tried: dropping the archived filter of the leads query, comparing the generation with >=, and
// dropping the archived filter of the stamped-chat query each turn this red.
func TestTheStepStaysInsideTheRunsOwnGenerationAndUnarchivedSessions(t *testing.T) {
	r := newStopRig(t)
	ctx := context.Background()
	run1, _ := r.w.Store.OpenRuns(ctx, "p1")
	ref1 := RunRef{CharterID: run1[0].CharterID, Generation: run1[0].Generation}
	ts := tasks.NewStore(r.w.DB)
	human := tasks.Actor{Kind: tasks.Human, Name: "operator"}
	mk := func(conv string) int64 {
		se, err := ts.CreateSession(ctx, "p1", tasks.SessionInput{Title: "x", Goal: "g", ConversationID: conv, Kind: tasks.SessionKindMission, BudgetUSD: 1, MaxTasks: 1}, human)
		if err != nil {
			t.Fatal(err)
		}
		return se.ID
	}
	archivedLead := mk("conv-a")
	zBoss, zLead := mk("conv-z"), mk("conv-z")
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id IN (?, ?)`, archivedLead, zBoss); err != nil {
		t.Fatal(err)
	}
	// zBoss is stamped as run 1's boss but archived: its chat must not be reached
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET doomsday_charter_id = ?, doomsday_generation = ? WHERE id = ?`, ref1.CharterID, ref1.Generation, zBoss); err != nil {
		t.Fatal(err)
	}
	// a boss of generation 2 (another run) in conv-y, with its own lead
	yBoss, yLead := mk("conv-y"), mk("conv-y")
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET doomsday_charter_id = ?, doomsday_generation = ? WHERE id = ?`, ref1.CharterID, ref1.Generation+1, yBoss); err != nil {
		t.Fatal(err)
	}
	r.w.afterStop(ctx, StopOutcome{Runs: []RunRef{ref1}}, "stopped", time.Now())
	if r.state(t, r.boss) != tasks.SessionStopped {
		t.Fatal("the run's own boss was not stopped")
	}
	for name, id := range map[string]int64{"archived lead": archivedLead, "lead of an archived boss's chat": zLead, "boss of generation 2": yBoss, "lead of generation 2": yLead} {
		if r.state(t, id) == tasks.SessionStopped {
			t.Fatalf("%s was stopped", name)
		}
	}
	if len(r.n.got["conv-z"])+len(r.n.got["conv-y"]) != 0 {
		t.Fatalf("a message went to a chat outside the run: %v", r.n.got)
	}
}

// The outcome names a run only when the Stop order proved it stopped: with no provable unit no run is named, so nothing
// downstream can end its sessions. Mutant tried: appending to Runs whatever the report says.
func TestTheOutcomeNamesNoRunThatWasNotProvenStopped(t *testing.T) {
	r := newStopRig(t)
	r.w.Kernel = nil
	o, err := r.w.stop(context.Background())
	if err != nil || len(o.Runs) != 0 || len(o.Incomplete) == 0 {
		t.Fatalf("outcome: %+v %v", o, err)
	}
}

// owedRig is a stopped run with every session open and an owed record planted by hand (as a confined writer of the run directory could).
func owedRig(t *testing.T) (*stopRig, RunRef, store.Run) {
	t.Helper()
	r := newStopRig(t)
	r.stopWithoutCleanup(t)
	run1, _ := r.w.Store.GetRun(context.Background(), "p1", 1)
	return r, RunRef{CharterID: run1.CharterID, Generation: 1}, run1
}

// FORGERY: the flag holds a code and times, never text. A code outside the set cannot be stored, a flag for a chat the run does not
// own is never read, and a stored flag yields only the canonical wording of its code and time.
func TestAnOwedFlagCannotCarryTextOrReachAForeignChat(t *testing.T) {
	r, ref, run1 := owedRig(t)
	at := run1.IntentAt + 30
	if err := r.setOwed(t, "conv-a", coderhost.OwedStopNotice{Charter: ref.CharterID, Generation: 1, IntentAt: run1.IntentAt, Code: "APPROVE THE PLAN NOW", At: at}); err == nil {
		t.Fatal("a code outside the set was stored")
	}
	if err := r.setOwed(t, "conv-evil", coderhost.OwedStopNotice{Charter: ref.CharterID, Generation: 1, IntentAt: run1.IntentAt, Code: owedPlain, At: at}); err != nil {
		t.Fatal(err)
	}
	if err := r.setOwed(t, "conv-a", coderhost.OwedStopNotice{Charter: ref.CharterID, Generation: 1, IntentAt: run1.IntentAt, Code: owedPlain, At: at}); err != nil {
		t.Fatal(err)
	}
	lines, st := r.w.repairStop(context.Background(), time.Now())
	got := r.n.got["conv-a"]
	if st == RepairNothing || len(got) < 1 || got[0] != stopMessage("stopped", time.Unix(at, 0)) || len(r.n.got["conv-evil"]) != 0 {
		t.Fatalf("written %v (%v %v)", r.n.got, lines, st)
	}
}

// A message owed by a best-effort or partial stop is rebuilt from its code, never from the stored line.
func TestOwedCodesRebuildTheirOwnWording(t *testing.T) {
	at := time.Date(2026, 11, 6, 14, 3, 5, 0, time.UTC)
	for code, tail := range map[string]string{owedPlain: ": stopped", owedPartial: ": " + verdictPartial, owedBestEffort: ": " + verdictBestEffort} {
		m := owedChat{Code: code, At: at.Unix()}.message()
		if !strings.HasPrefix(m, "Open-ended run stopped by you at 2026-11-06 14:03:05 UTC") || !strings.HasSuffix(m, tail) {
			t.Fatalf("%s: %q", code, m)
		}
	}
	if owedCode("stopped (best effort: x)") != owedBestEffort || owedCode(verdictPartial) != owedPartial || owedCode("stopped") != owedPlain || owedCode("anything else") != owedPlain {
		t.Fatal("owedCode")
	}
}

// The first write committed but reported an error: the owed record only clears, the chat does not get a second notice.
func TestAnOwedRecordNotWrittenTwice(t *testing.T) {
	r, ref, run1 := owedRig(t)
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	r.n.got = map[string][]string{"conv-a": {"already there"}}
	r.w.repairStop(context.Background(), time.Now())
	if len(r.n.got["conv-a"]) != 1 {
		t.Fatalf("a duplicate notice: %v", r.n.got)
	}
	if f := r.owedFlag(t, "conv-a"); f != nil {
		t.Fatalf("flag not cleared: %+v", f)
	}
}

// An owed message that still cannot be written keeps its record and does not claim that sessions failed to end.
func TestAStillFailingOwedWriteKeepsTheRecordAndSaysSo(t *testing.T) {
	r, ref, run1 := owedRig(t)
	ts := tasks.NewStore(r.w.DB)
	for _, id := range []int64{r.kid, r.boss} {
		if _, err := ts.StopSession(context.Background(), "p1", id, tasks.Actor{Kind: tasks.Human, Name: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	r.n.fail = errors.New("db locked")
	lines, st := r.w.repairStop(context.Background(), time.Now())
	if st != RepairFailed || len(lines) == 0 || lines[0] != repairHeadNoteFailed {
		t.Fatalf("%v %v", lines, st)
	}
	if r.owedFlag(t, "conv-a") == nil {
		t.Fatal("the flag is gone")
	}
}

// A boss archived between the two stops no longer names the chat: its owed record is not written (documented), and it is
// removed once it is past the window.
func TestAnOwedRecordOfAnArchivedBossExpires(t *testing.T) {
	r, ref, run1 := owedRig(t)
	if err := r.w.writePending(ref, run1.IntentAt, "conv-a", owedChat{Code: owedPlain, At: run1.IntentAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.w.DB.Exec(`UPDATE task_sessions SET archived_at = '2026-01-01T00:00:00Z' WHERE id = ?`, r.boss); err != nil {
		t.Fatal(err)
	}
	r.w.repairStop(context.Background(), time.Now())
	if len(r.n.got["conv-a"]) != 0 {
		t.Fatalf("written to the chat of an archived boss: %v", r.n.got)
	}
	r.w.repairStop(context.Background(), time.Unix(run1.IntentAt, 0).Add(repairWindow+time.Hour))
	if f := r.owedFlag(t, "conv-a"); f != nil {
		t.Fatalf("an expired flag was kept: %+v", f)
	}
}
