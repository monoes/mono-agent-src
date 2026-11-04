package governor

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// Tests never touch the real ~/.monoagent.
func TestMain(m *testing.M) { testhome.Main(m) }

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := storage.NewDatabase(t.TempDir() + "/gov.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	return db.DB
}

// seedRun writes a charter row (body is the stored JSON) and a running run row straight into the tables: the triggers
// under test read the charter body and the run state, nothing else.
func seedRun(t *testing.T, db *sql.DB, profile string, gen int64, limitsJSON string) {
	t.Helper()
	body := fmt.Sprintf(`{"profile":%q,"generation":%d,"limits":%s}`, profile, gen, limitsJSON)
	id := fmt.Sprintf("dd-%s-%d", profile, gen)
	mustExec(t, db, `INSERT INTO doomsday_charters(profile,generation,charter_id,canon_version,charter_hash,body,mac,created_at) VALUES (?,?,?,2,x'00',?,x'00',1)`, profile, gen, id, body)
	mustExec(t, db, `INSERT INTO doomsday_runs(profile,generation,charter_id,state,mac) VALUES (?,?,?,'running',x'00')`, profile, gen, id)
}

func mustExec(t *testing.T, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func wantRefused(t *testing.T, db *sql.DB, why, substr, q string, a ...any) {
	t.Helper()
	_, err := db.Exec(q, a...)
	if err == nil {
		t.Fatalf("%s: the statement was accepted", why)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("%s: refused for another reason: %v (want %q)", why, err, substr)
	}
}

func slot(db *sql.DB, kind, tree, member string) error {
	_, err := db.Exec(`INSERT INTO doomsday_slots(profile,generation,kind,tree,member_id,created_at) VALUES ('p',1,?,?,?,1)`, kind, tree, member)
	return err
}

func TestProcessSlotsStopAtTheCharterMaxConcurrentAndTheLastOneIsReusable(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":3,"live_per_tree":3}`)
	for i := 0; i < 3; i++ {
		if err := slot(db, "process", "t", fmt.Sprint("m", i)); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	if err := slot(db, "process", "t", "m3"); err == nil || !strings.Contains(err.Error(), "max_concurrent") {
		t.Fatalf("the slot above the cap was admitted: %v", err)
	}
	mustExec(t, db, `UPDATE doomsday_slots SET released_at = 2 WHERE member_id = 'm1'`)
	if err := slot(db, "process", "t", "m3"); err != nil {
		t.Fatalf("a released slot is not reusable: %v", err)
	}
	mustExec(t, db, `UPDATE doomsday_slots SET released_at = 2 WHERE member_id = 'm0'`)
	if err := slot(db, "process", "t", "m3"); err == nil {
		t.Fatal("one member took the same slot kind twice")
	}
}

func TestSessionSlotsAreCountedPerTreeAndNeedTheMembersProcessSlot(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":10,"live_per_tree":2}`)
	if err := slot(db, "session", "a", "m0"); err == nil || !strings.Contains(err.Error(), "process slot") {
		t.Fatalf("a session slot without the member's process slot: %v", err)
	}
	for i, tree := range []string{"a", "a", "b"} {
		m := fmt.Sprint("m", i)
		if err := slot(db, "process", tree, m); err != nil {
			t.Fatal(err)
		}
		if err := slot(db, "session", tree, m); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if err := slot(db, "process", "a", "m9"); err != nil {
		t.Fatal(err)
	}
	if err := slot(db, "session", "a", "m9"); err == nil || !strings.Contains(err.Error(), "live_per_tree") {
		t.Fatalf("the third live session of a tree was admitted: %v", err)
	}
	// A root and a tool process take process slots only, so they never count against live_per_tree.
	if err := slot(db, "process", "a", "tool1"); err != nil {
		t.Fatalf("a tool process counted against the session cap: %v", err)
	}
}

func TestSlotsNeedARunningRunAndAreNeverRemovedOrRewritten(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":5,"live_per_tree":5}`)
	if err := slot(db, "process", "t", "m1"); err != nil {
		t.Fatal(err)
	}
	wantRefused(t, db, "remove", "never deleted", "DELETE"+" FROM doomsday_slots")
	wantRefused(t, db, "move to another member", "only be released", `UPDATE doomsday_slots SET member_id = 'x'`)
	wantRefused(t, db, "move to another tree", "only be released", `UPDATE doomsday_slots SET tree = 'x', released_at = 3`)
	wantRefused(t, db, "born released", "running run", `INSERT INTO doomsday_slots(profile,generation,kind,tree,member_id,created_at,released_at) VALUES ('p',1,'process','t','m2',1,2)`)
	mustExec(t, db, `UPDATE doomsday_slots SET released_at = 2`)
	wantRefused(t, db, "make live again", "only be released", `UPDATE doomsday_slots SET released_at = NULL`)
	wantRefused(t, db, "a generation without a run", "doomsday", `INSERT INTO doomsday_slots(profile,generation,kind,tree,member_id,created_at) VALUES ('p',9,'process','t','m',1)`)
	mustExec(t, db, `UPDATE doomsday_runs SET state = 'stop-requested'`)
	if err := slot(db, "process", "t", "m3"); err == nil || !strings.Contains(err.Error(), "running run") {
		t.Fatalf("a slot was reserved after Stop was requested: %v", err)
	}
}

const insertQueue = `INSERT INTO doomsday_queue(profile,generation,lead,class,bytes,payload,payload_sha,state,enq_at,mac) VALUES (?,1,'l',?,?,?,zeroblob(32),'queued',1,x'01')`

func enqueue(db *sql.DB, profile, class, payload string) error {
	_, err := db.Exec(insertQueue, profile, class, len(payload), payload)
	return err
}

func TestQueueRowBoundIsTheCharterNumberOrTheDefault(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":5,"live_per_tree":5,"queue_max_rows":3}`)
	for i := 0; i < 3; i++ {
		if err := enqueue(db, "p", "normal", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := enqueue(db, "p", "normal", "x"); err == nil || !strings.Contains(err.Error(), "full (rows)") {
		t.Fatalf("the fourth row: %v", err)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM doomsday_queue`).Scan(&n)
	if n != 3 {
		t.Fatalf("a refused enqueue left %d rows", n)
	}
	// A charter that names nothing (every charter written before the fields existed) gets the default.
	seedRun(t, db, "q", 1, `{"max_concurrent":5,"live_per_tree":5}`)
	for i := 0; i < DefaultQueueMaxRows; i++ {
		if err := enqueue(db, "q", "normal", "x"); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	if err := enqueue(db, "q", "normal", "x"); err == nil || !strings.Contains(err.Error(), "full (rows)") {
		t.Fatalf("one row above the default: %v", err)
	}
}

func TestQueueByteBoundCountsThePayloadItselfAndIsPerRun(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":5,"live_per_tree":5,"queue_max_bytes":100}`)
	if err := enqueue(db, "p", "normal", strings.Repeat("a", 60)); err != nil {
		t.Fatal(err)
	}
	// Claiming fewer bytes than the payload holds is refused: the bound is the payload's size, not a number the writer states.
	wantRefused(t, db, "understated bytes", "payload size", insertQueue, "p", "normal", 1, strings.Repeat("b", 60))
	if err := enqueue(db, "p", "normal", strings.Repeat("c", 60)); err == nil || !strings.Contains(err.Error(), "full (bytes)") {
		t.Fatalf("a payload over the byte bound was admitted: %v", err)
	}
	if err := enqueue(db, "p", "normal", strings.Repeat("d", 40)); err != nil {
		t.Fatalf("a payload exactly filling the bound: %v", err)
	}
	var sum int
	db.QueryRow(`SELECT sum(length(CAST(payload AS BLOB))) FROM doomsday_queue`).Scan(&sum)
	if sum != 100 {
		t.Fatalf("stored payload bytes %d, want 100", sum)
	}
	// Leaving the queue frees its bytes (the payload is dropped), so a run is not starved by finished work.
	mustExec(t, db, `UPDATE doomsday_queue SET state = 'admitted', payload = '' WHERE qid = 1`)
	if err := enqueue(db, "p", "normal", strings.Repeat("e", 60)); err != nil {
		t.Fatalf("bytes of an admitted item still counted: %v", err)
	}
}

func TestQueueStatesMoveForwardOnceAndStopCancels(t *testing.T) {
	db := openDB(t)
	seedRun(t, db, "p", 1, `{"max_concurrent":5,"live_per_tree":5}`)
	for i := 0; i < 3; i++ {
		if err := enqueue(db, "p", "boss", "x"); err != nil {
			t.Fatal(err)
		}
	}
	wantRefused(t, db, "remove", "never deleted", "DELETE"+" FROM doomsday_queue")
	wantRefused(t, db, "born admitted", "born queued", `INSERT INTO doomsday_queue(profile,generation,lead,class,bytes,payload,payload_sha,state,enq_at,mac) VALUES ('p',1,'l','normal',1,'x',zeroblob(32),'admitted',1,x'01')`)
	wantRefused(t, db, "rewrite the lead", "forward once", `UPDATE doomsday_queue SET lead = 'other', state = 'admitted' WHERE qid = 1`)
	wantRefused(t, db, "rewrite the class", "forward once", `UPDATE doomsday_queue SET class = 'normal', state = 'admitted' WHERE qid = 1`)
	wantRefused(t, db, "rewrite the payload", "forward once", `UPDATE doomsday_queue SET payload = 'y', state = 'admitted' WHERE qid = 1`)
	wantRefused(t, db, "no state change", "forward once", `UPDATE doomsday_queue SET mac = x'02' WHERE qid = 1`)
	mustExec(t, db, `UPDATE doomsday_queue SET state = 'admitted', payload = '', mac = x'02' WHERE qid = 1`)
	wantRefused(t, db, "admitted back to queued", "forward once", `UPDATE doomsday_queue SET state = 'queued' WHERE qid = 1`)
	wantRefused(t, db, "admitted to cancelled", "forward once", `UPDATE doomsday_queue SET state = 'cancelled' WHERE qid = 1`)
	// Stop: nothing is admitted any more, but cancelling still works.
	mustExec(t, db, `UPDATE doomsday_runs SET state = 'stop-requested'`)
	wantRefused(t, db, "admit after stop", "running run", `UPDATE doomsday_queue SET state = 'admitted' WHERE qid = 2`)
	wantRefused(t, db, "enqueue after stop", "running run", insertQueue, "p", "normal", 1, "x")
	mustExec(t, db, `UPDATE doomsday_queue SET state = 'cancelled', payload = '', mac = x'03' WHERE state = 'queued'`)
	var left int
	db.QueryRow(`SELECT count(*) FROM doomsday_queue WHERE state = 'queued'`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d items still queued after the cancel", left)
	}
}

func TestGovernorStateIsBounded(t *testing.T) {
	db := openDB(t)
	mustExec(t, db, `INSERT INTO doomsday_governor_state(profile,generation,key,value) VALUES ('p',1,'backoff.x','1')`)
	wantRefused(t, db, "an empty key", "CHECK", `INSERT INTO doomsday_governor_state(profile,generation,key,value) VALUES ('p',1,'','1')`)
	wantRefused(t, db, "an oversized value", "CHECK", `INSERT INTO doomsday_governor_state(profile,generation,key,value) VALUES ('p',1,'k',?)`, strings.Repeat("v", 4097))
}
