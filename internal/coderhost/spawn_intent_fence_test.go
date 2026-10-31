package coderhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The round-4 rules of the spawn sweep (spawn_intent.go): it never adopts or deletes anything of a task session that
// is no longer a draft, a sweeping claim has a lease and a back-off, and every cleanup step is fenced on the claim's
// generation.

func (f *mcFix) at(d time.Duration) {
	base := time.Now()
	f.st.now = func() time.Time { return base.Add(d) }
}

func (f *mcFix) approveChildSession(t *testing.T) {
	t.Helper()
	var id int64
	if err := f.st.db.QueryRow(`SELECT id FROM task_sessions WHERE parent_session_id IS NOT NULL`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ts := tasks.NewStore(f.st.db)
	if _, err := ts.AddSessionTask(context.Background(), "p", id, tasks.SessionTaskInput{Title: "plan", Acceptance: "ok"}, opActor); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.ApproveSession(context.Background(), "p", id, opActor); err != nil {
		t.Fatalf("approving the pair: %v", err)
	}
}

func (f *mcFix) intentState(t *testing.T) (state string, attempts int64) {
	t.Helper()
	if err := f.st.db.QueryRow(`SELECT state, attempts FROM coder_spawn_intents WHERE spawner = ?`, f.lead.ConversationID).Scan(&state, &attempts); err != nil {
		t.Fatalf("the intent row: %v", err)
	}
	return state, attempts
}

// crashedSpawn leaves an intent with a named child and a draft pair, as a process that died after the task session.
func (f *mcFix) crashedSpawn(t *testing.T) spawnIntent {
	t.Helper()
	f.st.afterSpawnSessionCreated = func() { panic("simulated crash") }
	if !crashed(func() { _, _ = f.spawnSession(req("Docs direction", f.bigScope())) }) {
		t.Fatal("the crash hook did not run")
	}
	f.st.afterSpawnSessionCreated = nil
	var in spawnIntent
	if err := f.st.db.QueryRow(`SELECT profile_id, spawner, scope_hash, child, generation FROM coder_spawn_intents`).Scan(&in.profile, &in.spawner, &in.hash, &in.child, &in.gen); err != nil {
		t.Fatal(err)
	}
	return in
}

func (f *mcFix) wantPairKept(t *testing.T) {
	t.Helper()
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, want the approved one kept", n)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 1 {
		t.Errorf("%d open child task sessions, want the approved one kept", n)
	}
	if n := acceptances(t, f); n != 0 {
		t.Errorf("%d acceptance rows; only finishSpawn writes one", n)
	}
}

// (2) The operator approves the pair while the spawn is slow; the sweep claims it, finds a session that is not a
// draft, touches nothing and holds the intent for the operator; the spawn's own finish then fails and its undo does
// not delete the conversation either.
func TestASweepNeverAdoptsOrDeletesAnApprovedSessionOfASlowSpawn(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() {
		f.approveChildSession(t)
		f.afterLease(time.Minute)
		_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	}
	if _, err := f.spawnSession(req("Docs direction", f.bigScope())); err == nil {
		t.Fatal("the spawn reported success after the sweep claimed its intent")
	}
	f.wantPairKept(t)
	if st, _ := f.intentState(t); st != "needs-operator" {
		t.Errorf("the intent is %q, want needs-operator", st)
	}
	if _, err := f.spawnSession(req("Another direction", f.bigScope())); !errors.Is(err, ErrSpawnPending) {
		t.Errorf("a further spawn of the lead: %v, want ErrSpawnPending", err)
	}
}

// (2) The spawn's undo alone refuses to delete a conversation whose task session is no longer a draft.
func TestTheSpawnsUndoKeepsAConversationWhoseSessionWasApproved(t *testing.T) {
	f := newMC(t, true)
	f.st.afterSpawnSessionCreated = func() {
		f.approveChildSession(t)
		if _, err := f.st.db.Exec(`UPDATE coder_spawn_intents SET generation = generation + 1`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.spawnSession(req("Docs direction", f.bigScope())); err == nil {
		t.Fatal("the spawn reported success although its finish failed")
	}
	f.wantPairKept(t)
}

// (2) The same through the sweep of a crashed spawn whose session was approved before the lease ran out.
func TestTheSweepOfACrashedSpawnLeavesAnApprovedSessionAndHoldsTheIntent(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t)
	f.approveChildSession(t)
	f.afterLease(time.Minute)
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	f.wantPairKept(t)
	if st, _ := f.intentState(t); st != "needs-operator" {
		t.Errorf("the intent is %q, want needs-operator", st)
	}
	f.afterLease(time.Hour)
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20) // held rows are not swept again
	f.wantPairKept(t)
}

// (3) A sweeping row cannot be claimed again within its lease; a claim with a stale generation never works.
func TestASweepingRowIsNotReclaimedWithinItsLeaseNorWithAStaleGeneration(t *testing.T) {
	f := newMC(t, true)
	old := f.crashedSpawn(t)
	f.at(SpawnIntentLease + time.Minute)
	lc, cc := f.st.now().Add(-SpawnIntentLease).UTC(), f.st.now().Add(-SpawnCreateLeases*SpawnIntentLease).UTC()
	first, ok, err := f.st.claimIntent(old, lc, cc)
	if err != nil || !ok {
		t.Fatalf("the first claim: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := f.st.claimIntent(first, lc, cc); ok {
		t.Error("a second sweep re-claimed a sweeping row inside its lease")
	}
	f.at(SpawnIntentLease + 24*time.Hour) // far past any lease
	lc = f.st.now().Add(-SpawnIntentLease).UTC()
	if _, ok, _ := f.st.claimIntent(old, lc, cc); ok {
		t.Error("a claim with the stale generation succeeded")
	}
	if _, ok, _ := f.st.claimIntent(first, lc, cc); !ok {
		t.Error("the claim with the current generation after the lease was refused")
	}
}

// (3) A cleanup step carrying a stale generation changes nothing.
func TestAFencedCleanupStepWithAStaleGenerationChangesNothing(t *testing.T) {
	f := newMC(t, true)
	old := f.crashedSpawn(t)
	f.afterLease(time.Minute)
	lc, cc := f.st.now().Add(-SpawnIntentLease).UTC(), f.st.now().Add(-SpawnCreateLeases*SpawnIntentLease).UTC()
	if _, ok, err := f.st.claimIntent(old, lc, cc); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	old.child = f.childOf(t)
	if err := f.st.endSpawn(context.Background(), old); err == nil {
		t.Error("a cleanup with a stale generation reported success")
	}
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, want the stale cleanup to remove nothing", n)
	}
	if n := countRows(t, f.st, openChildTaskSessions); n != 1 {
		t.Errorf("%d open child task sessions, want the stale cleanup to stop nothing", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents, want the claimed one kept", n)
	}
}

func (f *mcFix) childOf(t *testing.T) string {
	t.Helper()
	var c string
	if err := f.st.db.QueryRow(`SELECT conversation_id FROM coder_sessions WHERE role = 'child'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return c
}

// (5) A cleanup that keeps failing is retried with a growing back-off, and after SpawnSweepAttempts claims the intent
// is held for the operator: no further attempts and a lead that stays blocked.
func TestACleanupThatKeepsFailingBacksOffThenHoldsTheIntentForTheOperator(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t)
	if _, err := f.st.db.Exec(`CREATE TRIGGER rv_stuck BEFORE DELETE ON coder_spawn_intents BEGIN SELECT RAISE(ABORT, 'stuck'); END`); err != nil {
		t.Fatal(err)
	}
	sweep := func() { _, _ = f.st.SweepSpawnIntents(context.Background(), 20) }
	f.at(SpawnIntentLease + time.Minute)
	sweep()
	if _, n := f.intentState(t); n != 1 {
		t.Fatalf("attempts %d after the first pass, want 1", n)
	}
	sweep() // the same instant: inside the back-off
	if _, n := f.intentState(t); n != 1 {
		t.Errorf("attempts %d after an immediate second pass, want 1 (back-off)", n)
	}
	for i := 0; i < 10; i++ {
		f.at(time.Duration(i+1) * 48 * time.Hour)
		sweep()
	}
	st, n := f.intentState(t)
	if st != "needs-operator" || n != int64(SpawnSweepAttempts) {
		t.Errorf("after many passes: state %q attempts %d, want needs-operator after %d", st, n, SpawnSweepAttempts)
	}
}

// T1: a spawn's own claim of its row changes nothing once the sweep has claimed it.
func TestEndSpawnNowOfASpawnWhoseRowWasClaimedByTheSweepDoesNothing(t *testing.T) {
	f := newMC(t, true)
	old := f.crashedSpawn(t)
	f.afterLease(time.Minute)
	lc, cc := f.st.now().Add(-SpawnIntentLease).UTC(), f.st.now().Add(-SpawnCreateLeases*SpawnIntentLease).UTC()
	claimed, ok, err := f.st.claimIntent(old, lc, cc)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := f.st.endSpawnNow(context.Background(), old); err != nil {
		t.Fatalf("endSpawnNow: %v", err)
	}
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations; the spawn's late cleanup must not run", n)
	}
	var gen int64
	if err := f.st.db.QueryRow(`SELECT generation FROM coder_spawn_intents`).Scan(&gen); err != nil || gen != claimed.gen {
		t.Errorf("generation %d (err %v), want the sweep's %d untouched", gen, err, claimed.gen)
	}
}

// The child write is guarded by the pending state as well as the generation.
func TestSetIntentChildRefusesARowThatIsNotPending(t *testing.T) {
	f := newMC(t, true)
	in, err := f.st.openSpawnIntent("p", f.lead.ConversationID, "h")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping'`); err != nil {
		t.Fatal(err)
	}
	if err := f.st.setIntentChild(in, "x"); !errors.Is(err, errIntentGone) {
		t.Errorf("setIntentChild on a sweeping row: %v, want errIntentGone", err)
	}
}

// The delete itself is guarded in its own transaction: a live task session that the draft scan did not see (here it
// is not linked below the spawner's session) still keeps the conversation, and the intent is held.
func TestTheSweepsDeleteGuardKeepsAConversationWithALiveSessionTheScanMissed(t *testing.T) {
	f := newMC(t, true)
	f.crashedSpawn(t)
	f.approveChildSession(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET parent_session_id = NULL WHERE parent_session_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	f.afterLease(time.Minute)
	_, _ = f.st.SweepSpawnIntents(context.Background(), 20)
	if n := countRows(t, f.st, childConversations); n != 1 {
		t.Errorf("%d child conversations, want the one with a live session kept", n)
	}
	if st, _ := f.intentState(t); st != "needs-operator" {
		t.Errorf("the intent is %q, want needs-operator", st)
	}
}
