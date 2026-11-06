package doomsday

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
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

func (n *notes) exists(_ context.Context, conv string, _ int64) (bool, error) {
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

// A stop whose message could not be written: the next stop writes it (once), exits 0; if that write fails too the exit is
// non-zero and the next one still gets to write it.
func TestASecondStopWritesTheMessageTheFirstCouldNot(t *testing.T) {
	r := newStopRig(t)
	r.n.fail = errors.New("db locked")
	if out, err := run(t, r.deps, "", "stop"); clicore.ExitCodeFor(err) != ExitCleanupFailed || !strings.HasPrefix(out, "stopped\n") || !strings.Contains(out, "transcript NOT written") {
		t.Fatalf("first: %v %q", err, out)
	}
	out, err := run(t, r.deps, "", "stop") // still failing
	if clicore.ExitCodeFor(err) != ExitCleanupFailed || !strings.Contains(out, "transcript NOT written (conv-a)") || !strings.HasPrefix(out, "no open run;") {
		t.Fatalf("failing repair must exit non-zero and say what failed: %v %q", err, out)
	}
	r.n.fail = nil
	out, err = run(t, r.deps, "", "stop")
	if err != nil || len(r.n.got["conv-a"]) != 1 || !strings.Contains(out, "transcript conv-a: Open-ended run stopped by you at ") {
		t.Fatalf("repair: %v %q %v", err, out, r.n.got)
	}
	if out, err = run(t, r.deps, "", "stop"); err == nil {
		t.Fatalf("nothing left, yet success: %q", out)
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
