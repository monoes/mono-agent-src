package coderhost

import (
	"errors"
	"testing"
	"time"
)

func questionOf(t *testing.T, st *Store, id int64) Question {
	t.Helper()
	q, err := st.GetQuestion("p", id)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// masterAnswer parks a risky question on child k1 and has its master answer it.
func masterAnswer(t *testing.T, st *Store) (Question, Command) {
	t.Helper()
	childOf(t, st)
	q, err := st.Park("p", "k1", "risky_action", "force push?", nil, "", false)
	if err != nil {
		t.Fatal(err)
	}
	_, cmd, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"})
	if err != nil {
		t.Fatal(err)
	}
	return q, cmd
}

func TestTimedOutDefaultsClaimOnlyTheRemainingHeadroom(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	setSettings(t, st, "a", `{"autonomy_classes": {"ambiguity": {"who": "user", "fallback": "take_default"}}, "checkin_limit": 1}`)
	var ids []int64
	for i := 0; i < 3; i++ {
		q, err := st.Park("p", "a", "ambiguity", "which?", nil, "d", false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, q.ID)
	}
	clk.t = clk.t.Add(ParkTimeout + time.Second)
	if n, err := st.ApplyDefaults(); n != 1 || err != nil {
		t.Fatalf("applied %d, %v", n, err)
	}
	open := 0
	for _, id := range ids {
		if questionOf(t, st, id).Status == "open" {
			open++
		}
	}
	if open != 2 {
		t.Fatalf("%d questions still open, want 2", open)
	}
	if n, _ := st.ApplyDefaults(); n != 0 { // the first note is still waiting: it counts on the next pass too
		t.Fatalf("a later pass applied %d", n)
	}
}

func TestAMasterAnswerCancelledByAStopReopensTheQuestion(t *testing.T) {
	st, _ := parkStore(t)
	q, cmd := masterAnswer(t, st)
	if _, err := st.CancelPendingSends("p", "k1", cmd.ID+1, "cancelled by a stop"); err != nil {
		t.Fatal(err)
	}
	st.ReopenFailedAnswers()
	got := questionOf(t, st, q.ID)
	if got.Status != "open" || got.CommandID != 0 {
		t.Fatalf("question = %+v", got)
	}
	if x, _, _ := st.GetSession("p", "k1"); x.State != StateWaitingUser {
		t.Fatalf("state = %s", x.State)
	}
	if _, _, err := st.Answer("p", q.ID, "no", masterScope{conv: "m"}); err != nil {
		t.Fatalf("the reopened question could not be answered again: %v", err)
	}
}

func TestAnExpiredAnswerNoteReopensTheQuestion(t *testing.T) {
	st, clk := parkStore(t)
	q, _ := masterAnswer(t, st)
	clk.t = clk.t.Add(MaxCheckinAge + time.Minute)
	if err := st.ExpireSends(); err != nil {
		t.Fatal(err)
	}
	st.ReopenFailedAnswers()
	if got := questionOf(t, st, q.ID); got.Status != "open" {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestADefaultThatNeverRanIsMarkedNotAppliedAndTheQuestionReopens(t *testing.T) {
	st, clk := parkStore(t)
	addConversation(t, st, "a", "/w/a")
	q := defaultable(t, st, clk, "a")
	if n, err := st.ApplyDefaults(); n != 1 || err != nil {
		t.Fatalf("applied %d, %v", n, err)
	}
	cmds := answerCommands(t, st, "a")
	if len(cmds) != 1 {
		t.Fatalf("notes = %d", len(cmds))
	}
	st.SetCommand(cmds[0].ID, CmdFailed, "cancelled by a stop") //nolint:errcheck
	st.ReopenFailedAnswers()
	if got := questionOf(t, st, q.ID); got.Status != "open" || got.Answer != "" {
		t.Fatalf("question = %+v", got)
	}
	l, _ := st.ListDecisions("p", "a", LedgerFilter{})
	if len(l) != 1 || l[0].Status != DecisionNotApplied {
		t.Fatalf("ledger = %+v", l)
	}
	if _, err := st.RevertDecision("p", "a", l[0].ID, "operator"); !errors.Is(err, ErrNotReversible) {
		t.Fatalf("a not-applied default was revertable: %v", err)
	}
}

func TestAPersonsAnswerThatFailsAtRunTimeReopensTheQuestion(t *testing.T) {
	h, _, st := newHost(t)
	addConversation(t, st, "a", "/w/a")
	q, _ := st.Park("p", "a", "risky_action", "go?", nil, "", false)
	if _, _, err := st.Answer("p", q.ID, "yes", OperatorScope{}); err != nil {
		t.Fatal(err)
	}
	x, _, _ := st.GetSession("p", "a")
	x.State = StateStopped // stopped between the answer and the run
	if err := st.SaveSession(x); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick() // the note fails: the session is stopped
	_ = h.Tick() // and its question comes back
	if got := questionOf(t, st, q.ID); got.Status != "open" {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestMasterAnswerFollowsTheMastersState(t *testing.T) {
	h, l, st := newHost(t)
	q, cmd := masterAnswer(t, st)
	m, _, _ := st.GetSession("p", "m")
	m.State = StatePaused
	if err := st.SaveSession(m); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdPending {
		t.Fatalf("a paused master's note ran or failed: %+v", got)
	}
	m.State = StateIdle
	if err := st.SaveSession(m); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdDone || len(l.started) != 1 {
		t.Fatalf("the note did not run after the resume: %+v, started %v", got, l.started)
	}
	_ = q
}

func TestStoppedMasterCancelsItsAnswerAndGivesTheQuestionBack(t *testing.T) {
	h, l, st := newHost(t)
	q, cmd := masterAnswer(t, st)
	m, _, _ := st.GetSession("p", "m")
	m.State = StateStopped
	if err := st.SaveSession(m); err != nil {
		t.Fatal(err)
	}
	_ = h.Tick()
	if got, _, _ := st.GetCommand(cmd.ID); got.Status != CmdFailed || len(l.started) != 0 {
		t.Fatalf("a stopped master's note ran: %+v", got)
	}
	_ = h.Tick()
	if got := questionOf(t, st, q.ID); got.Status != "open" {
		t.Fatalf("status = %s", got.Status)
	}
	if _, _, err := st.Answer("p", q.ID, "yes", masterScope{conv: "m"}); !errors.Is(err, ErrMasterStopped) {
		t.Fatalf("a stopped master answered: %v", err)
	}
}
