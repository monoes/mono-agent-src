package tasks

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// finishedWorkerTask is a session task a worker claimed, commented on, attached a commit to and
// finished: it sits in Review, its claim is gone, and it holds the evidence.
func finishedWorkerTask(t *testing.T, s *Store) (Session, Task) {
	t.Helper()
	se, tk := claimedTask(t, s, "", "task")
	w := bot("w1")
	if _, err := s.SessionComment(bg, "default", se.ID, tk.ID, "halfway", w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, ArtifactInput{Kind: "commit", Ref: "abc123", Note: "the change"}, w); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, Outcome{Result: "done, see the commit"}, w); err != nil {
		t.Fatal(err)
	}
	return se, tk
}

// dump is every row of the task's evidence tables as text, to compare before and after.
func dump(t *testing.T, db *sql.DB, task int64) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT * FROM tasks WHERE id = ?`,
		`SELECT * FROM task_events WHERE task_id = ? ORDER BY id`,
		`SELECT * FROM task_artifacts WHERE task_id = ? ORDER BY id`,
	} {
		rows, err := db.Query(q, task)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", vals)
		}
		rows.Close()
	}
	return b.String()
}

func retiredEvents(t *testing.T, db *sql.DB, session int64) []string {
	t.Helper()
	rows, err := db.Query(`SELECT actor, note FROM task_session_events WHERE session_id = ? AND kind = 'worker_retired' ORDER BY id`, session)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, n string
		if err := rows.Scan(&a, &n); err != nil {
			t.Fatal(err)
		}
		out = append(out, a+": "+n)
	}
	return out
}

var liveTokenSeq int

func liveMissionToken(t *testing.T, db *sql.DB, session, task int64, expires time.Time, revoked bool) {
	t.Helper()
	rev := any(nil)
	if revoked {
		rev = "2026-10-05T11:00:00.000Z"
	}
	liveTokenSeq++
	id := fmt.Sprintf("atk_%d", liveTokenSeq)
	_, err := db.Exec(`INSERT INTO agent_tokens (id, profile_id, name, scope, prefix, token_hash, created_at, expires_at, revoked_at,
		mission_session, mission_task, mission_role, mission_actor, mission_owner)
		VALUES (?, 'default', ?, 'read', 'p', ?, '2026-10-05T11:00:00.000Z', ?, ?, ?, ?, 'worker', 'w1', 'o')`,
		id, "mission-"+id, "h"+id, expires.UTC().Format("2006-01-02T15:04:05.000Z"), rev, session, task)
	if err != nil {
		t.Fatal(err)
	}
}

// The operator retires the idle worker of a task: the audit event is written, the evidence stays
// byte for byte, and the result says it was done now.
func TestRetireWorkerRecordsItAndKeepsTheEvidence(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, tk := finishedWorkerTask(t, s)
	before := dump(t, db, tk.ID)

	r, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "stale context", human)
	if err != nil {
		t.Fatal(err)
	}
	if r.AlreadyRetired || r.Session != se.ID || r.Task != tk.ID {
		t.Fatalf("result = %+v", r)
	}
	ev := retiredEvents(t, db, se.ID)
	if len(ev) != 1 || ev[0] != fmt.Sprintf("you: task #%d: stale context", tk.ID) {
		t.Fatalf("audit events = %q", ev)
	}
	if after := dump(t, db, tk.ID); after != before {
		t.Fatalf("the task's evidence changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Retiring twice is a no-op that says so; it writes no second event.
func TestRetireWorkerTwiceIsANoOp(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, tk := finishedWorkerTask(t, s)
	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); err != nil {
		t.Fatal(err)
	}
	r, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human)
	if err != nil || !r.AlreadyRetired {
		t.Fatalf("second retire = %+v, %v", r, err)
	}
	if ev := retiredEvents(t, db, se.ID); len(ev) != 1 {
		t.Fatalf("a second event was written: %q", ev)
	}
}

// A worker that still holds the claim, or a mission token that is still live, is not idle: nothing is
// written. The check and the write are one transaction.
func TestRetireWorkerRefusesAWorkerThatIsNotIdle(t *testing.T) {
	s, db, clk := newTestStore(t)
	se, tk := claimedTask(t, s, "", "task") // w1 holds it
	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a claimed task was retired: %v", err)
	}
	if ev := retiredEvents(t, db, se.ID); len(ev) != 0 {
		t.Fatalf("an event was written for a refused retire: %q", ev)
	}

	se2, tk2 := finishedWorkerTask(t, s)
	liveMissionToken(t, db, se2.ID, tk2.ID, clk.now().Add(time.Hour), false)
	if _, err := s.RetireWorker(bg, "default", se2.ID, tk2.ID, "", human); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a worker with a live token was retired: %v", err)
	}
	if ev := retiredEvents(t, db, se2.ID); len(ev) != 0 {
		t.Fatalf("an event was written for a refused retire: %q", ev)
	}
}

// A revoked or expired token is not live.
func TestRetireWorkerIgnoresRevokedAndExpiredTokens(t *testing.T) {
	s, db, clk := newTestStore(t)
	se, tk := finishedWorkerTask(t, s)
	liveMissionToken(t, db, se.ID, tk.ID, clk.now().Add(time.Hour), true)
	liveMissionToken(t, db, se.ID, tk.ID, clk.now().Add(-time.Hour), false)
	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); err != nil {
		t.Fatalf("retire with only dead tokens: %v", err)
	}
}

// Only the operator retires a worker; an agent, a bound agent and a capture are refused and nothing is written.
func TestRetireWorkerIsTheOperatorsAlone(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, tk := finishedWorkerTask(t, s)
	for name, a := range map[string]Actor{
		"agent":        bot("lead"),
		"bound agent":  {Kind: Agent, Name: "w1", Session: se.ID, Task: tk.ID},
		"session lead": {Kind: Agent, Name: "lead", Session: se.ID},
		"verifier":     {Kind: Agent, Name: "v", Session: se.ID, Task: tk.ID, Verifier: true},
		"capture":      {Kind: Capture},
	} {
		if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", a); !errors.Is(err, ErrOperatorOnly) {
			t.Errorf("%s: err = %v, want ErrOperatorOnly", name, err)
		}
	}
	if ev := retiredEvents(t, db, se.ID); len(ev) != 0 {
		t.Fatalf("a refused actor wrote an event: %q", ev)
	}
}

// A task nobody worked, a task of another session and an unknown task are refused.
func TestRetireWorkerNeedsAWorkerOfThatSessionsTask(t *testing.T) {
	s, _, _ := newTestStore(t)
	se := mustSession(t, s, "default", "s")
	untouched := mustTask(t, s, "default", se.ID, "nobody worked this")
	mustRun(t, s, "default", se)
	if _, err := s.RetireWorker(bg, "default", se.ID, untouched.ID, "", human); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a task no worker took: %v", err)
	}
	other, tk := finishedWorkerTask(t, s)
	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); err == nil {
		t.Fatalf("a task of another session (%d) was retired through session %d", other.ID, se.ID)
	}
	if _, err := s.RetireWorker(bg, "default", se.ID, 99999, "", human); err == nil {
		t.Fatal("an unknown task was retired")
	}
}

func storedWorker(t *testing.T, db *sql.DB, conv, agent string, session, task int64) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at) VALUES (?, 'default', 'monomind', 'n', 'n')`, conv); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ai_chat_org_workers (profile_id, conversation_id, agent_id, turn_id, runtime, session_id, report, outcome, updated_at, mission_session, mission_task)
		VALUES ('default', ?, ?, 't', 'claude', 'sess-1', 'the report', 'done', 'n', ?, ?)`, conv, agent, session, task); err != nil {
		t.Fatal(err)
	}
}

func storedState(t *testing.T, db *sql.DB, conv, agent string) string {
	t.Helper()
	var sess, report, outcome string
	if err := db.QueryRow(`SELECT session_id, report, outcome FROM ai_chat_org_workers WHERE conversation_id = ? AND agent_id = ?`, conv, agent).Scan(&sess, &report, &outcome); err != nil {
		t.Fatal(err)
	}
	return sess + "|" + report + "|" + outcome
}

// The retirement drops the stored worker's session so the lead cannot resume it in a later turn,
// keeps its report, and leaves other tasks' workers alone. The same transaction writes the event.
func TestRetireWorkerEndsTheStoredWorkerOfThatTaskOnly(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, tk := finishedWorkerTask(t, s)
	other := mustTask(t, s, "default", se.ID, "another step")
	storedWorker(t, db, "conv", "w1", se.ID, tk.ID)
	storedWorker(t, db, "conv", "w2", se.ID, other.ID)
	storedWorker(t, db, "conv", "w3", 0, 0) // not a mission worker

	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); err != nil {
		t.Fatal(err)
	}
	if got := storedState(t, db, "conv", "w1"); got != "|the report|retired" {
		t.Fatalf("the retired worker = %q", got)
	}
	for _, a := range []string{"w2", "w3"} {
		if got := storedState(t, db, "conv", a); got != "sess-1|the report|done" {
			t.Fatalf("%s was touched: %q", a, got)
		}
	}
	// A second retire reports it, and still ends a stored worker that appeared in between (one
	// saved by a turn that was running while the first retire ran).
	storedWorker(t, db, "conv", "w4", se.ID, tk.ID)
	r, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human)
	if err != nil || !r.AlreadyRetired {
		t.Fatalf("second retire = %+v, %v", r, err)
	}
	if got := storedState(t, db, "conv", "w4"); got != "|the report|retired" {
		t.Fatalf("the late stored worker = %q", got)
	}
}

// A refused retire (a live claim) changes no stored worker.
func TestRetireWorkerRefusedLeavesTheStoredWorker(t *testing.T) {
	s, db, _ := newTestStore(t)
	se, tk := claimedTask(t, s, "", "task")
	storedWorker(t, db, "conv", "w1", se.ID, tk.ID)
	if _, err := s.RetireWorker(bg, "default", se.ID, tk.ID, "", human); err == nil {
		t.Fatal("a claimed task was retired")
	}
	if got := storedState(t, db, "conv", "w1"); got != "sess-1|the report|done" {
		t.Fatalf("the stored worker changed: %q", got)
	}
}
