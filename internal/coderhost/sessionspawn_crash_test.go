package coderhost

import (
	"context"
	"errors"
	"testing"
	"time"

)

// crashed runs fn and reports whether it panicked: the injected crash between the task session and the ledger write.
func crashed(fn func()) (did bool) {
	defer func() { did = recover() != nil }()
	fn()
	return false
}

// A process that dies after the task session is committed and before the acceptance is written (simulated by a panic
// at exactly that point, with nothing after it running) leaves a child nobody was told about. Reopening the stores:
// a retry of the same request is refused while the first is unresolved (it cannot make a second child), and the
// host's sweep, once the lease has passed, removes the orphan pair; the request can then be made once.
func TestACrashBetweenTheTaskSessionAndTheLedgerIsRecoveredAndNeverDuplicates(t *testing.T) {
	f := newMC(t, true)
	r := req("Docs direction", f.bigScope())
	f.st.afterSpawnSessionCreated = func() { panic("simulated crash") }
	if !crashed(func() { _, _ = f.spawnSession(r) }) {
		t.Fatal("the crash hook did not run")
	}
	st2 := NewStore(f.st.db) // the stores reopened: no memory of the first call
	children := func() int {
		return countRows(t, st2, `SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id = ?`, f.lead.ConversationID)
	}
	open := func() int {
		return countRows(t, st2, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL AND state NOT IN ('stopped','done','failed')`)
	}
	accepted := func() int {
		return countRows(t, st2, `SELECT COUNT(*) FROM coder_session_decisions WHERE conversation_id = ? AND choice = '`+AdviceAccept+`'`, f.lead.ConversationID)
	}
	if children() != 1 || open() != 1 || accepted() != 0 {
		t.Fatalf("after the crash: %d child conversations, %d open child task sessions, %d acceptance rows; the test expected the orphan pair with no ledger row", children(), open(), accepted())
	}
	// the retry, at once: refused, not a second child
	_, err := st2.SpawnSession(context.Background(), f.scope(), "p", r)
	if err == nil || !errors.Is(err, ErrLimit) {
		t.Errorf("a retry while the first spawn is unresolved: %v, want a refusal that is a limit", err)
	}
	if n := children(); n != 1 {
		t.Errorf("%d child conversations after the retry, want the one orphan only", n)
	}
	// after the lease the sweep removes the orphan pair and the request can be made, once
	st2.now = func() time.Time { return time.Now().Add(SpawnIntentLease + time.Minute) }
	if _, err := st2.SweepSpawnIntents(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if children() != 0 || open() != 0 || accepted() != 0 {
		t.Errorf("after the sweep: %d child conversations, %d open child task sessions, %d acceptance rows; want none", children(), open(), accepted())
	}
	if n := countRows(t, st2, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents left after the sweep", n)
	}
	if _, err := st2.SpawnSession(context.Background(), f.scope(), "p", r); err != nil {
		t.Fatalf("the request after the recovery: %v", err)
	}
	if children() != 1 || accepted() != 1 || open() != 1 {
		t.Errorf("after the retry: %d children, %d accepted, %d open task sessions, want 1 each", children(), accepted(), open())
	}
}

// An acceptance that cannot be recorded is refused and cleaned up completely: the child conversation and its carve are
// gone, the draft task session is stopped, and nothing stays pending. When even the stop fails, the unresolved intent
// stays so that the sweep finishes the cleanup (and a retry meanwhile is refused).
func TestAFailedAcceptanceCleansUpEverythingAndKeepsTheIntentIfTheCleanupFailed(t *testing.T) {
	f := newMC(t, true)
	if _, err := f.st.db.Exec(`CREATE TRIGGER rv_ledger BEFORE INSERT ON coder_session_decisions BEGIN SELECT RAISE(ABORT, 'ledger down'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`CREATE TRIGGER rv_stop BEFORE UPDATE OF state ON task_sessions WHEN NEW.state = 'stopped' AND OLD.parent_session_id IS NOT NULL BEGIN SELECT RAISE(ABORT, 'stop down'); END`); err != nil {
		t.Fatal(err)
	}
	r := req("Docs direction", f.bigScope())
	if _, err := f.spawnSession(r); err == nil {
		t.Fatal("the spawn was made")
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_sessions WHERE parent_conversation_id != ''`); n != 0 {
		t.Errorf("%d child conversations left", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL AND state NOT IN ('stopped','done','failed')`); n != 1 {
		t.Fatalf("%d open child task sessions; the test set the stop to fail, so one should be open", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 1 {
		t.Errorf("%d intents; the cleanup failed, so the intent must stay for the sweep", n)
	}
	if _, err := f.spawnSession(r); err == nil || !errors.Is(err, ErrLimit) {
		t.Errorf("a retry while the cleanup is unfinished: %v", err)
	}
	// the stop works again; after the lease the sweep finishes the cleanup
	if _, err := f.st.db.Exec(`DROP TRIGGER rv_stop`); err != nil {
		t.Fatal(err)
	}
	f.st.now = func() time.Time { return time.Now().Add(SpawnIntentLease + time.Minute) }
	if _, err := f.st.SweepSpawnIntents(context.Background(), 20); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM task_sessions WHERE parent_session_id IS NOT NULL AND state NOT IN ('stopped','done','failed')`); n != 0 {
		t.Errorf("%d open child task sessions after the sweep", n)
	}
	if n := countRows(t, f.st, `SELECT COUNT(*) FROM coder_spawn_intents`); n != 0 {
		t.Errorf("%d intents after the sweep", n)
	}
}
