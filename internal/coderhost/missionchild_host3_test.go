package coderhost

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// stopRowAt records a done stop command queued at one time and completed at another.
func (f *hostFix) stopRowAt(t *testing.T, conv, issuer string, queued, done time.Time) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, status, created_at, updated_at, issuer)
		VALUES ('p', ?, 'stop', '', '', 'done', ?, ?, ?)`, conv, queued.UTC().Format(timeLayout), done.UTC().Format(timeLayout), issuer); err != nil {
		t.Fatal(err)
	}
}

// F1: a stop is dated by when it completed. One queued while the turn was being admitted, before the turn row
// exists, still stopped that turn.
func TestAStopQueuedBeforeTheTurnRowButDoneAfterItCounts(t *testing.T) {
	now := time.Now()
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	addTurn(t, f.st, out.ConversationID, "t-child", "completed", "x", now)              // the turn row appears at now
	f.stopRowAt(t, out.ConversationID, "", now.Add(-time.Minute), now.Add(time.Minute)) // queued before it, done after it
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndStopped {
		t.Fatalf("child row = %+v, want stopped: the stop completed after its turn began", r)
	}
	// the same for the lead
	g := newHostFix(t)
	g.idleTaskChild(t, 0, 0)
	addTurn(t, g.st, g.lead.ConversationID, "t-lead", "completed", "x", now)
	g.stopRowAt(t, g.lead.ConversationID, "", now.Add(-time.Minute), now.Add(time.Minute))
	g.h.missionChildPass()
	if r := g.row(t, 0); r.EndReason != EndLeadStop {
		t.Fatalf("lead cascade missed a stop completed after the lead's turn began: %+v", r)
	}
	// a stop that completed before the turn began says nothing about it, whenever it was queued
	h := newHostFix(t)
	o := h.idleTaskChild(t, 0, 0)
	addTurn(t, h.st, o.ConversationID, "t-child", "completed", "x", now)
	h.stopRowAt(t, o.ConversationID, "", now.Add(-2*time.Minute), now.Add(-time.Minute))
	h.h.missionChildPass()
	if r := h.row(t, 0); r.EndReason != EndNoFinish {
		t.Fatalf("a stop that finished before the child's turn began ended it: %+v", r)
	}
}

// F2: a stop of the lead ends the children that existed when it ran, not those spawned after it.
func TestAStaleLeadStopDoesNotKillALaterChild(t *testing.T) {
	f := newHostFix(t)
	f.stopRowAt(t, f.lead.ConversationID, "", time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)) // the lead idled since
	addTurn(t, f.st, f.lead.ConversationID, "t-lead", "completed", "x", time.Now().Add(-3*time.Hour))
	f.idleTaskChild(t, 0, 0) // spawned after the stop
	f.h.missionChildPass()
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason == EndLeadStop {
		t.Fatalf("a child spawned after the lead's stop was cascade-stopped: %+v", r)
	}
}

// F3: the lead's facts are read once per lead per pass.
func TestLeadFactsAreReadOncePerLeadPerPass(t *testing.T) {
	f := newHostFix(t)
	f.addTasks(t, 2)
	f.setPolicy(t, f.scope(), PolicyPatch{MaxChildren: ip(3)})
	for i := 0; i < 3; i++ {
		f.idleTaskChild(t, i, 0)
	}
	f.h.missionChildPass()
	if len(f.h.leadFacts) != 1 {
		t.Fatalf("lead facts = %d entries for one lead with three rows", len(f.h.leadFacts))
	}
	// within a pass the lead is read once: a stop that appears after the first row is not seen by the next
	row := f.row(t, 0)
	row.SpawnedAt = time.Now().Add(-time.Hour).UTC().Format(timeLayout)
	f.h.leadFacts = map[string]leadFact{}
	if f.h.leadStopSince(row) {
		t.Fatal("no stop yet")
	}
	f.stopRowAt(t, f.lead.ConversationID, "", time.Now(), time.Now())
	if f.h.leadStopSince(row) {
		t.Fatal("the lead was read again within one pass")
	}
	f.h.leadFacts = map[string]leadFact{}
	if !f.h.leadStopSince(row) {
		t.Fatal("the next pass does not see the stop")
	}
	var plan string
	rows, err := f.st.db.Query(`EXPLAIN QUERY PLAN SELECT updated_at FROM coder_session_commands WHERE profile_id = 'p' AND conversation_id = 'x' AND kind = 'stop' AND status = 'done' ORDER BY id DESC LIMIT 20`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c int
		var detail string
		_ = rows.Scan(&a, &b, &c, &detail)
		plan += detail + ";"
	}
	if !strings.Contains(plan, "idx_coder_session_commands_chat") {
		t.Fatalf("the stop lookup does not use the conversation index: %s", plan)
	}
}

// F4: a master's held stop does not stand in for the pass's own.
func TestAMastersPendingStopDoesNotSuppressTheCascade(t *testing.T) {
	f := newHostFix(t)
	out := f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil { // the child's turn starts
		t.Fatal(err)
	}
	if _, err := f.st.ControlChild(f.lead, out.ConversationID, KindStop); err != nil { // the lead's own stop, still pending
		t.Fatal(err)
	}
	x := state(t, f.st, f.lead.ConversationID)
	x.State = StateStopped
	if err := f.st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if n := rowsOf(t, f.st, `SELECT id FROM coder_session_commands WHERE kind = 'stop' AND status = 'pending' AND issuer = '' AND turn_id = '`+cascadeTurn+`'`); n == "" {
		t.Fatal("the pass queued no stop of its own because a master's stop was waiting")
	}
	f.finish()
}

// Survivors: two passes queue one stop; a session marked stopped ends its row as stopped.
func TestTwoPassesQueueOneStop(t *testing.T) {
	f := newHostFix(t)
	f.task(t, 0, 0)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	x := state(t, f.st, f.lead.ConversationID)
	x.State = StateStopped
	if err := f.st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	f.h.missionChildPass()
	n := rowsOf(t, f.st, `SELECT id FROM coder_session_commands WHERE kind = 'stop' AND status = 'pending'`)
	if n == "" || strings.Count(n, "\n") != 0 {
		t.Fatalf("pending stops = %q, want exactly one", n)
	}
	f.finish()
}

func TestAChildSessionMarkedStoppedEndsStopped(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	f.ranTurn(t, out.ConversationID)
	x := state(t, f.st, out.ConversationID)
	x.State = StateStopped
	if err := f.st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndStopped {
		t.Fatalf("row = %+v, want stopped for a session marked stopped", r)
	}
}

// F5: a failed delete leaves the reservation to the host's stale pass, with the pending conversation still named.
func TestAFailedDeleteLeavesTheReservationForTheStalePass(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{BudgetUSD: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`CREATE TRIGGER no_sends BEFORE INSERT ON coder_session_commands BEGIN SELECT RAISE(ABORT, 'no room'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`CREATE TRIGGER no_delete BEFORE DELETE ON ai_chat_conversations BEGIN SELECT RAISE(ABORT, 'cannot delete'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if err == nil || !strings.Contains(err.Error(), "left for the host to recover") {
		t.Fatalf("err = %v", err)
	}
	row := f.row(t, 0)
	kid := rowsOf(t, f.st, `SELECT conversation_id FROM coder_sessions WHERE role = 'child'`)
	if row.State != ChildSpawning || row.PendingConversationID == "" || row.PendingConversationID != kid {
		t.Fatalf("row = %+v with child %q: the reservation must stay, naming the conversation it could not delete", row, kid)
	}
	// the host's stale pass ends the row, releases the carve and stops the conversation
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(30 * time.Minute) }
	f.h.missionChildPass()
	row = f.row(t, 0)
	if row.State != ChildFailed || row.EndReason != EndExpired || row.ChildConversationID != kid {
		t.Fatalf("after the stale pass: %+v", row)
	}
	if state(t, f.st, kid).State != StateStopped {
		t.Fatal("the conversation the failed delete left was not stopped")
	}
	if c, _ := f.st.Carved("p", f.lead.ConversationID); c > 2*minCap {
		t.Fatalf("carved = %v: the orphan keeps its carve", c)
	}
}

// F6: a mission with no lead conversation (an unlink) is unknown, not closed; only a different lead is.
func TestAnUnlinkedMissionDoesNotCascade(t *testing.T) {
	f := newHostFix(t)
	f.idleTaskChild(t, 0, 0)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET conversation_id = NULL WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason == EndLeadStop {
		t.Fatalf("a transient unlink ended the lead's child: %+v", r)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET conversation_id = '' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason == EndLeadStop {
		t.Fatalf("an empty link ended the lead's child: %+v", r)
	}
}

// F8: the orphan report forgets sessions that are gone.
func TestOrphanReportForgetsSessionsThatAreGone(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	f.mark(t, 0)
	kid, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 2})
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).UTC().Format(timeLayout)
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET created_at = ? WHERE conversation_id = ?`, old, kid.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE coder_session_commands SET status = 'done'`); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if len(f.h.orphanSeen) != 1 {
		t.Fatalf("orphanSeen = %v", f.h.orphanSeen)
	}
	if err := mustAI(t, f.st).DeleteConversation(kid.ConversationID, "p"); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if len(f.h.orphanSeen) != 0 {
		t.Fatalf("orphanSeen = %v after the session was deleted", f.h.orphanSeen)
	}
	_ = errors.New
}
