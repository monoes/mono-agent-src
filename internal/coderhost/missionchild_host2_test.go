package coderhost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// idleTaskChild starts task i's child and leaves it idle: its first message ran and was taken.
func (f *hostFix) idleTaskChild(t *testing.T, i int, usd float64) Spawned {
	t.Helper()
	out := f.task(t, i, usd)
	if _, err := f.st.db.Exec(`UPDATE coder_session_commands SET status = 'done' WHERE conversation_id = ?`, out.ConversationID); err != nil {
		t.Fatal(err)
	}
	return out
}

// stopRow records a done stop command for a conversation, as the host leaves one after it stopped a turn.
func (f *hostFix) stopRow(t *testing.T, conv, issuer, turn string, at time.Time) {
	t.Helper()
	if _, err := f.st.db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, status, created_at, updated_at, issuer)
		VALUES ('p', ?, 'stop', ?, '', 'done', ?, ?, ?)`, conv, turn, at.UTC().Format(timeLayout), at.UTC().Format(timeLayout), issuer); err != nil {
		t.Fatal(err)
	}
}

// ---- the lead's stop is a stop command, not only a session state

func TestOperatorStopOfTheLeadCascades(t *testing.T) {
	f := newHostFix(t)
	f.idleTaskChild(t, 0, 0)
	f.stopRow(t, f.lead.ConversationID, "", "", time.Now().Add(time.Minute)) // a person stopped the lead's turn
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State != ChildEnded || r.EndReason != EndLeadStop {
		t.Fatalf("row = %+v, want ended/lead_stopped after the operator's stop of the lead", r)
	}
}

func TestOnlyAPersonsFreshStopOfTheLeadCascades(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name  string
		setup func(f *hostFix, child string)
	}{
		{"a stop issued by another session", func(f *hostFix, child string) {
			f.stopRow(t, f.lead.ConversationID, "some-other-master", "", now.Add(time.Minute))
		}},
		{"the pass's own stop", func(f *hostFix, child string) {
			f.stopRow(t, f.lead.ConversationID, "", cascadeTurn, now.Add(time.Minute))
		}},
		{"a stop from before the lead's last turn", func(f *hostFix, child string) {
			f.stopRow(t, f.lead.ConversationID, "", "", now.Add(time.Minute))
			addTurn(t, f.st, f.lead.ConversationID, "t-lead", "completed", "x", now.Add(time.Hour))
		}},
		{"the lead's own stop of its child", func(f *hostFix, child string) { f.stopRow(t, child, f.lead.ConversationID, "", now.Add(time.Minute)) }},
	}
	for _, c := range cases {
		f := newHostFix(t)
		out := f.idleTaskChild(t, 0, 0)
		c.setup(f, out.ConversationID)
		f.h.missionChildPass()
		if r := f.row(t, 0); r.EndReason == EndLeadStop {
			t.Errorf("%s cascaded: %+v", c.name, r)
		}
	}
}

func TestMissionRelinkedToAnotherLeadCountsAsClosed(t *testing.T) {
	f := newHostFix(t)
	f.idleTaskChild(t, 0, 0)
	other, _ := master(t, f.st, "workspace")
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET conversation_id = ? WHERE id = ?`, other.ConversationID, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndLeadStop {
		t.Fatalf("row = %+v: a mission handed to another conversation must end its old lead's children", r)
	}
}

// ---- end reasons

func TestAChildThatFinishedAndWasThenInterruptedIsDone(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	f.ranTurn(t, out.ConversationID)
	f.stopRow(t, out.ConversationID, "", "", time.Now().Add(time.Minute))
	f.setTask(t, 0, "review")
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndDone {
		t.Fatalf("row = %+v, want done: a stop does not mask a task that was handed back", r)
	}
}

func TestALeadsOwnStopOfItsChildEndsStoppedAndAnOldStopDoesNot(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	f.ranTurn(t, out.ConversationID) // a turn that began a minute ago
	f.stopRow(t, out.ConversationID, f.lead.ConversationID, "", time.Now().Add(time.Minute))
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndStopped {
		t.Fatalf("row = %+v, want stopped for the lead's own stop", r)
	}
	// a stop from before the child's last turn says nothing about how that turn ended
	g := newHostFix(t)
	o2 := g.idleTaskChild(t, 0, 0)
	g.stopRow(t, o2.ConversationID, "", "", time.Now().Add(-time.Hour))
	g.ranTurn(t, o2.ConversationID)
	g.h.missionChildPass()
	if r := g.row(t, 0); r.EndReason != EndNoFinish {
		t.Fatalf("row = %+v, want no_finish: the stop is older than the child's last turn", r)
	}
	// the pass's own stop is not a person's
	h := newHostFix(t)
	o3 := h.idleTaskChild(t, 0, 0)
	h.ranTurn(t, o3.ConversationID)
	h.stopRow(t, o3.ConversationID, "", cascadeTurn, time.Now().Add(time.Minute))
	h.h.missionChildPass()
	if r := h.row(t, 0); r.EndReason != EndNoFinish {
		t.Fatalf("row = %+v, want no_finish: the pass's own stops are not counted", r)
	}
}

func TestAChildWhoseCapIsUsedUpEndsAsBudget(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	out := f.idleTaskChild(t, 0, 2)
	f.ranTurn(t, out.ConversationID)
	// a started turn that reported no cost is charged its whole cap: the child has nothing left
	if err := f.st.RecordTurnCap("p", out.ConversationID, "t-spent", 2); err != nil {
		t.Fatal(err)
	}
	f.st.MarkTurnCapStarted("p", out.ConversationID, "t-spent")
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndBudget {
		t.Fatalf("row = %+v, want budget", r)
	}
}

// ---- the spawn's steps

func TestSpawnNotesThePendingConversationAndTouchesTheReservation(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`CREATE TABLE touch_log (n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	// only a Touch changes the clock of a spawning row and nothing else about it
	if _, err := f.st.db.Exec(`CREATE TRIGGER log_touch AFTER UPDATE ON coder_mission_children
		WHEN NEW.state = 'spawning' AND OLD.state = 'spawning' AND NEW.updated_at <> OLD.updated_at AND NEW.pending_conversation_id = OLD.pending_conversation_id
		BEGIN INSERT INTO touch_log VALUES (1); END`); err != nil {
		t.Fatal(err)
	}
	out := f.task(t, 0, 0)
	row := f.row(t, 0)
	if row.PendingConversationID != out.ConversationID {
		t.Fatalf("pending conversation = %q, want %q: the spawn did not note it when it created the conversation", row.PendingConversationID, out.ConversationID)
	}
	if n := rowsOf(t, f.st, `SELECT n FROM touch_log`); n == "" {
		t.Fatal("the spawn never refreshed its reservation before queueing the first message")
	}
}

func TestADeadSpawnAfterTheConversationWasMadeIsExpiredNotRetried(t *testing.T) {
	// the hook is what makes this possible: with the pending conversation noted, the stale row is ended
	f := newHostFix(t)
	f.mark(t, 0)
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[0]); err != nil {
		t.Fatal(err)
	}
	made := spawnNoMessage(t, f.st, f.lead)
	if err := f.st.NotePending(f.scope(), "p", f.se.ID, f.ids[0], made); err != nil {
		t.Fatal(err)
	}
	real := time.Now()
	f.st.now = func() time.Time { return real.Add(30 * time.Minute) }
	f.h.missionChildPass()
	if r := f.row(t, 0); r.EndReason != EndExpired {
		t.Fatalf("row = %+v", r)
	}
}

func TestDepthLimitReadsAsFullAndNothingIsCounted(t *testing.T) {
	f := newHostFix(t)
	s, _ := f.st.GetSettings("p", f.lead.ConversationID)
	s.MaxDepth = 1
	if err := f.st.SaveSettings("p", f.lead.ConversationID, s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief"); !errors.Is(err, ErrChildrenFull) {
		t.Fatalf("a depth refusal: %v, want the typed full error", err)
	}
	if r := f.row(t, 0); r.State != ChildMarked || r.Attempts != 0 {
		t.Fatalf("row = %+v", r)
	}
}

func TestARefusalOfTheMasterBeforeAnythingIsMadeIsNotAnAttempt(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[0], MarkOptions{}); err != nil {
		t.Fatal(err)
	}
	// the lead's conversation row is gone, so the spawn refuses before it makes anything
	if _, err := f.st.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE ai_chat_conversations SET profile_id = 'elsewhere' WHERE id = ?`, f.lead.ConversationID); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.SpawnForTask(f.scope(), "p", f.se.ID, f.ids[0], "brief")
	if !errors.Is(err, ErrNotYours) {
		t.Fatalf("err = %v", err)
	}
	if r := f.row(t, 0); r.State != ChildMarked || r.Attempts != 0 {
		t.Fatalf("a refusal before anything was made cost an attempt: %+v", r)
	}
}

// ---- stale turns do not pile stops up

func TestAStaleTurnIdIsClearedNotStoppedForever(t *testing.T) {
	f := newHostFix(t)
	out := f.idleTaskChild(t, 0, 0)
	f.ranTurn(t, out.ConversationID)
	// the session names a turn this host does not have
	if _, err := f.st.db.Exec(`UPDATE coder_sessions SET state = 'running', turn_id = 'ghost', turn_pid = 4242 WHERE conversation_id = ?`, out.ConversationID); err != nil {
		t.Fatal(err)
	}
	x := state(t, f.st, f.lead.ConversationID)
	x.State = StateStopped
	if err := f.st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		f.h.missionChildPass()
	}
	if n := rowsOf(t, f.st, `SELECT id FROM coder_session_commands WHERE kind = 'stop'`); n != "" {
		t.Fatalf("stops were queued for a turn that does not exist: %q", n)
	}
	if r := f.row(t, 0); r.State != ChildEnded || r.EndReason != EndLeadStop {
		t.Fatalf("row = %+v: the row must end once the stale turn is cleared", r)
	}
	f.l.mu.Lock()
	finished := strings.Join(f.l.finished, ",")
	f.l.mu.Unlock()
	if !strings.Contains(finished, "ghost") {
		t.Fatalf("the launcher was not told the stale turn is finished (as Recover does): %q", finished)
	}
}

// ---- the pass is driven by Tick

func TestTickRunsADuePassAndOnlyWhenDue(t *testing.T) {
	f := newHostFix(t)
	base := time.Now()
	clock := base
	f.st.now = func() time.Time { return clock }
	a := f.idleTaskChild(t, 0, 0)
	f.ranTurn(t, a.ConversationID)
	f.setTask(t, 0, "review")
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if r := f.row(t, 0); r.State != ChildEnded {
		t.Fatalf("Tick did not run a due pass: %+v", r)
	}
	f.addTasks(t, 1)
	b := f.idleTaskChild(t, 1, 0)
	f.ranTurn(t, b.ConversationID)
	f.setTask(t, 1, "review")
	clock = base.Add(time.Second)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if r := f.row(t, 1); r.State != ChildSpawned {
		t.Fatalf("Tick ran a pass again within MissionChildEvery: %+v", r)
	}
	clock = base.Add(MissionChildEvery + time.Second)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if r := f.row(t, 1); r.State != ChildEnded {
		t.Fatalf("Tick did not run the pass once it was due again: %+v", r)
	}
}

// ---- orphans are reported, never stopped

func TestOrphanCarveIsReportedOnceAndNothingIsStopped(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	f.mark(t, 0) // the lead has a policy
	var logs []string
	f.h.Logf = func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	kid, err := f.st.Spawn(f.lead, SpawnRequest{BudgetUSD: 2}) // a child in no row
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
	f.h.missionChildPass()
	n := 0
	for _, l := range logs {
		if strings.Contains(l, kid.ConversationID) && strings.Contains(l, "is in no mission row") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the orphaned carve was reported %d times: %v", n, logs)
	}
	if state(t, f.st, kid.ConversationID).State == StateStopped {
		t.Fatal("the pass stopped a child on age and policy alone")
	}
	// the lead can still use it
	if _, err := f.st.SendToChild(f.lead, kid.ConversationID, "hello"); err != nil {
		t.Fatalf("the lead cannot send to its own child: %v", err)
	}
}
