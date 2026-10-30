package coderhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The interleavings of a slow spawn and the host's sweep (spawn_intent.go): whoever claims the intent row first
// wins, the other side refuses or skips, and a successful spawn never has its child removed.

const (
	openChildTaskSessions = `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL AND state NOT IN ('stopped','done','failed')`
	childConversations    = `SELECT COUNT(*) FROM coder_sessions WHERE role = 'child'`
)

func acceptances(t *testing.T, f *mcFix) int {
	t.Helper()
	return countRows(t, f.st, `SELECT COUNT(*) FROM coder_session_decisions WHERE conversation_id = ? AND choice = '`+AdviceAccept+`'`, f.lead.ConversationID)
}

func (f *mcFix) afterLease(extra time.Duration) {
	f.st.now = func() time.Time { return time.Now().Add(SpawnIntentLease + extra) }
}

// (1) The sweep claims the intent after the spawn made its pair and before it finished: the finish refuses, nothing
// is written to the ledger, and the spawn undoes itself. No call succeeds with a removed child.
func TestASweepThatClaimsBeforeTheFinishMakesTheSpawnRefuseAndUndoItself(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() {
		f.afterLease(time.Minute)
		if n, err := f.st.SweepSpawnIntents(context.Background(), 20); err != nil || n != 1 {
			t.Errorf("the sweep ended %d (err %v), want 1", n, err)
		}
	}
	_, err := f.spawnSession(req("Docs direction", f.bigScope()))
	if err == nil {
		t.Fatal("the spawn reported success although the sweep had removed its child")
	}
	if n := countRows(t, f.st, childConversations); n != 0 {
		t.Errorf("%d child conversations left", n)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 0 {
		t.Errorf("%d open child task sessions left", n)
	}
	if n := acceptances(t, f); n != 0 {
		t.Errorf("%d acceptance rows; the ledger must not hold a spawn that was undone", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents left", n)
	}
}

// (2) The spawn finishes between the sweep's read and its cleanup: the sweep's claim changes no row, so it removes
// nothing, and the spawn's caller keeps a child that still exists.
func TestASpawnThatFinishesBetweenTheSweepsReadAndItsCleanupIsNotRemoved(t *testing.T) {
	f := newMC(t, true)
	created, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var out SessionSpawned
	f.st.afterSpawnSessionCreated = func() { close(created); <-release }
	go func() {
		var err error
		out, err = f.spawnSession(req("Docs direction", f.bigScope()))
		done <- err
	}()
	<-created
	f.afterLease(time.Minute)
	f.st.afterSweepRead = func() { // the spawn finishes now, after the sweep's snapshot
		close(release)
		if err := <-done; err != nil {
			t.Errorf("the spawn: %v", err)
		}
	}
	n, err := f.st.SweepSpawnIntents(context.Background(), 20)
	if err != nil || n != 0 {
		t.Fatalf("the sweep ended %d (err %v), want 0: its claim must fail", n, err)
	}
	if x, ok, _ := f.st.GetSession("p", out.ConversationID); !ok || x.Role != "child" {
		t.Errorf("the child %s the spawn reported is gone (ok=%v)", out.ConversationID, ok)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 1 {
		t.Errorf("%d open child task sessions, want the spawn's one", n)
	}
	if n := acceptances(t, f); n != 1 {
		t.Errorf("%d acceptance rows, want 1", n)
	}
}

// (3) A replacement intent for the same lead is not touched by the old spawn: its child write and its finish
// refuse, and the new row stays as it was.
func TestAnOldSpawnCannotTouchTheReplacementIntent(t *testing.T) {
	f := newMC(t, true)
	c := f.lead
	old, err := f.st.openSpawnIntent("p", c.ConversationID, "h1")
	if err != nil {
		t.Fatal(err)
	}
	f.afterLease(4 * SpawnIntentLease) // past the longer lease of an unnamed child
	if n, err := f.st.SweepSpawnIntents(context.Background(), 20); err != nil || n != 1 {
		t.Fatalf("sweep ended %d, err %v", n, err)
	}
	f.st.now = time.Now
	repl, err := f.st.openSpawnIntent("p", c.ConversationID, "h1") // the same scope, a new spawn
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.setIntentChild(old, "stale-child"); !errors.Is(err, errIntentGone) {
		t.Errorf("the old spawn's child write: %v, want errIntentGone", err)
	}
	if err := f.st.finishSpawn(Caller{ProfileID: "p", ConversationID: c.ConversationID}, "t", SessionSpawnRequest{Kind: tasks.SessionKindTask}, SpawnFacts{}, "w", old); !errors.Is(err, errIntentGone) {
		t.Errorf("the old spawn's finish: %v, want errIntentGone", err)
	}
	if n := acceptances(t, f); n != 0 {
		t.Errorf("%d acceptance rows from the old spawn", n)
	}
	var child, state string
	var gen int64
	if err := f.st.db.QueryRow(`SELECT child, state, generation FROM coder_spawn_intents WHERE spawner = ?`, c.ConversationID).Scan(&child, &state, &gen); err != nil {
		t.Fatalf("the replacement intent is gone: %v", err)
	}
	if child != "" || state != "pending" || gen != repl.gen {
		t.Errorf("the replacement intent became child=%q state=%q gen=%d, want it untouched", child, state, gen)
	}
}

// (4) At most one unresolved spawn per lead, whatever the request: a different title is refused while one is pending.
func TestASecondSpawnWithAnotherRequestIsRefusedWhileOneIsPending(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() { panic("simulated crash") }
	if !crashed(func() { _, _ = f.spawnSession(req("First direction", f.bigScope())) }) {
		t.Fatal("the crash hook did not run")
	}
	f.st.afterSpawnSessionCreated = nil
	_, err := f.spawnSession(req("A different direction", f.bigScope()))
	if !errors.Is(err, ErrSpawnPending) {
		t.Fatalf("a second request while one is pending: %v, want ErrSpawnPending", err)
	}
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, want the first one only", n)
	}
}

// (5) A child write that changes no row fails the spawn before it goes any further: no task session is ever made.
func TestASpawnWhoseIntentIsGoneWhenTheChildIsNamedFailsAndMakesNoTaskSession(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER rv_intent AFTER INSERT ON coder_sessions WHEN NEW.role = 'child' OR NEW.conversation_id != '' BEGIN UPDATE coder_spawn_intents SET generation = generation + 1; END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.spawnSession(req("Docs direction", f.bigScope()))
	if err == nil || !errors.Is(err, errIntentGone) {
		t.Fatalf("the spawn: %v, want errIntentGone", err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL`); n != 0 {
		t.Errorf("%d child task sessions were made after the child write failed", n)
	}
	if n := countRows(t, f.st, childConversations); n != 0 {
		t.Errorf("%d child conversations left", n)
	}
}

// An intent whose child is still unnamed is kept for the longer lease; one with a child is swept at the plain lease.
func TestAnIntentWithoutAChildWaitsForTheLongerLease(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.openSpawnIntent("p", f.lead.ConversationID, "h"); err != nil {
		t.Fatal(err)
	}
	f.afterLease(time.Minute)
	if n, _ := f.st.SweepSpawnIntents(context.Background(), 20); n != 0 {
		t.Fatalf("an unnamed child's intent was swept at the plain lease")
	}
	f.afterLease(SpawnCreateLeases * SpawnIntentLease)
	if n, err := f.st.SweepSpawnIntents(context.Background(), 20); err != nil || n != 1 {
		t.Fatalf("after the longer lease: swept %d, err %v", n, err)
	}
}

// The host's Tick runs the sweep.
func TestTickSweepsAnInterruptedSpawn(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() { panic("simulated crash") }
	if !crashed(func() { _, _ = f.spawnSession(req("Docs direction", f.bigScope())) }) {
		t.Fatal("the crash hook did not run")
	}
	h := &Host{Store: f.st, L: newFakeLauncher()}
	_ = h.Tick()
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Fatalf("a Tick inside the lease removed the pair (%d children)", n)
	}
	f.afterLease(time.Minute)
	_ = h.Tick()
	if n := countRows(t, f.st, childConversations); n != 0 {
		t.Errorf("%d child conversations after a Tick past the lease", n)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 0 {
		t.Errorf("%d open child task sessions after the Tick", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents after the Tick", n)
	}
}

// A task session the operator approved after the interruption is left alone and gets its missing acceptance row.
func TestTheSweepLeavesAnApprovedPairAndRecordsItsAcceptance(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() { panic("simulated crash") }
	if !crashed(func() { _, _ = f.spawnSession(req("Docs direction", f.bigScope())) }) {
		t.Fatal("the crash hook did not run")
	}
	var id int64
	if err := f.st.db.QueryRow(`SELECT id FROM task_sessions WHERE parent_session_id IS NOT NULL`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.NewStore(f.st.db).AddSessionTask(context.Background(), "p", id, tasks.SessionTaskInput{Title: "plan", Acceptance: "ok"}, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.NewStore(f.st.db).ApproveSession(context.Background(), "p", id, opActor); err != nil {
		t.Fatalf("approving the pair: %v", err)
	}
	f.afterLease(time.Minute)
	if n, err := f.st.SweepSpawnIntents(context.Background(), 20); err != nil || n != 1 {
		t.Fatalf("sweep ended %d, err %v", n, err)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 1 {
		t.Errorf("%d open child task sessions, want the approved one kept", n)
	}
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, want the approved one's kept", n)
	}
	if n := acceptances(t, f); n != 1 {
		t.Errorf("%d acceptance rows, want the missing one written", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents left", n)
	}
}
