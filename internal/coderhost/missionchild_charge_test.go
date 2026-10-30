package coderhost

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// The spend of an ended child is added to the mission's meter once (phase P-B2).

func (f *hostFix) missionSpent(t *testing.T) float64 {
	t.Helper()
	var v float64
	if err := f.st.db.QueryRow(`SELECT spent_usd FROM task_sessions WHERE id = ?`, f.se.ID).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *hostFix) chargeRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.st.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_events WHERE kind = 'charge' AND session_id = ?`, f.se.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// endedChild spawns task i with a budget wish, gives the child a cost, and readies its row to end as done.
func (f *hostFix) endedChild(t *testing.T, i int, cost float64) string {
	t.Helper()
	out := f.task(t, i, 4)
	f.ranTurn(t, out.ConversationID)
	event(t, f.st, out.ConversationID, "t-"+out.ConversationID, 1, "usage.updated", fmt.Sprintf(`{"agentId":"","costUsd":%v}`, cost))
	if _, err := f.st.db.Exec(`UPDATE coder_session_commands SET status = 'done' WHERE conversation_id = ?`, out.ConversationID); err != nil {
		t.Fatal(err)
	}
	f.setTask(t, i, "review")
	return out.ConversationID
}

func TestAnEndedChildsSpendIsChargedToTheMissionExactlyOnce(t *testing.T) {
	f := newHostFix(t)
	if err := f.st.SetBudget("p", f.lead.ConversationID, 10); err != nil {
		t.Fatal(err)
	}
	conv := f.endedChild(t, 0, 1.25)
	if got := f.missionSpent(t); got != 0 {
		t.Fatalf("spent before the child ended = %v", got)
	}
	f.h.missionChildPass()
	if r := f.row(t, 0); r.State != ChildEnded {
		t.Fatalf("row = %+v", r)
	}
	if got := f.missionSpent(t); got != 1.25 {
		t.Fatalf("mission spent = %v, want the child's 1.25", got)
	}
	// settle again, by every route: the pass, the sweep, a direct settle, a second host over the same store
	f.h.missionChildPass()
	f.h.settleChild("p", conv)
	f.h.settleEnded()
	(&Host{Store: f.st, L: newFakeLauncher()}).missionChildPass()
	if got := f.missionSpent(t); got != 1.25 {
		t.Fatalf("mission spent after re-settling = %v: the child was charged more than once", got)
	}
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows, want 1", n)
	}
	if usd, err := f.st.ChargeChild("p", conv); err != nil || usd != 0 {
		t.Fatalf("ChargeChild again = %v, %v", usd, err)
	}
}

func TestSettlesOnTheSameChildChargeOnce(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 2)
	f.h.missionChildPass()
	for i := 0; i < 6; i++ {
		f.h.settleChild("p", conv)
	}
	if got := f.missionSpent(t); got != 2 {
		t.Fatalf("mission spent = %v, want 2", got)
	}
}

// A mission that stopped (its budget halt, an operator) still pays for the child that ends afterwards, and the new
// total is what the budget check reads.
func TestAChildThatEndsWhileTheMissionIsHaltingIsStillCharged(t *testing.T) {
	for _, state := range []string{"stopped", "failed", "done"} {
		t.Run(state, func(t *testing.T) {
			f := newHostFix(t)
			conv := f.endedChild(t, 0, 1.25)
			if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10, spent_usd = 9.5, state = ? WHERE id = ?`, state, f.se.ID); err != nil {
				t.Fatal(err)
			}
			f.h.missionChildPass()
			se, err := f.ts.GetSession(context.Background(), "p", f.se.ID, opActor)
			if err != nil {
				t.Fatal(err)
			}
			if se.SpentUSD != 10.75 || se.SpentUSD < se.BudgetUSD {
				t.Fatalf("spent = %v of %v: the child's 1.25 was not added, so the halt would not see it", se.SpentUSD, se.BudgetUSD)
			}
			if n := f.chargeRows(t); n != 1 {
				t.Fatalf("%d charge rows for %s", n, conv)
			}
		})
	}
}

func TestALiveChildIsNotCharged(t *testing.T) {
	f := newHostFix(t)
	out := f.task(t, 0, 3)
	f.ranTurn(t, out.ConversationID)
	event(t, f.st, out.ConversationID, "t-"+out.ConversationID, 1, "usage.updated", `{"agentId":"","costUsd":1}`)
	if usd, err := f.st.ChargeChild("p", out.ConversationID); err != nil || usd != 0 || f.missionSpent(t) != 0 || f.chargeRows(t) != 0 {
		t.Fatalf("a live child was charged: %v %v spent %v rows %d", usd, err, f.missionSpent(t), f.chargeRows(t))
	}
}

// A host that was down when a child ended settles it on its next pass.
func TestTheSweepChargesAnEndedRowNobodySettled(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 0.75)
	if ended, err := f.st.End("p", conv, EndDone); err != nil || !ended {
		t.Fatalf("End = %v, %v", ended, err)
	}
	if f.missionSpent(t) != 0 {
		t.Fatal("End alone must not write the task session (the host settles after it)")
	}
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 0.75 {
		t.Fatalf("spent = %v, want 0.75", got)
	}
}

func TestAMissionThatIsGoneIsNotChargedAndNotRetriedForever(t *testing.T) {
	f := newHostFix(t)
	conv := f.endedChild(t, 0, 1)
	if _, err := f.st.End("p", conv, EndDone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`INSERT OR IGNORE INTO profiles (id, name) VALUES ('elsewhere', 'E')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET profile_id = 'elsewhere' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.h.missionChildPass()
	f.h.missionChildPass()
	if n := f.chargeRows(t); n != 1 {
		t.Fatalf("%d charge rows for a mission that is gone, want one record that it was skipped", n)
	}
}

// ---- task tokens end with the child's run ----

type fakeRevoker struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (r *fakeRevoker) RevokeChildTokens(profile, conv string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, profile+"/"+conv)
	return r.err
}

func (r *fakeRevoker) revoked(conv string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if strings.HasSuffix(c, "/"+conv) {
			return true
		}
	}
	return false
}

func TestTheChildsTokensAreRevokedWhenItsTurnEnds(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{}
	f.h.ChildTokens = rv
	out := f.task(t, 0, 3)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if rv.revoked(out.ConversationID) {
		t.Fatal("revoked while the turn runs")
	}
	f.finish()
	if !rv.revoked(out.ConversationID) {
		t.Fatalf("the turn ended and its token was not revoked: %v", rv.calls)
	}
}

// stubbornLauncher starts turns that do not end when stopped, as a real turn takes a while to.
type stubbornLauncher struct{ *fakeLauncher }

type stubbornTurn struct{ Turn }

func (stubbornTurn) Stop() {}

func (l stubbornLauncher) Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (Turn, error) {
	t, err := l.fakeLauncher.Start(profileID, conversationID, turnID, message, attachments, missionToken)
	if err != nil {
		return nil, err
	}
	return stubbornTurn{t}, nil
}

func TestAStoppedChildsTokensAreRevokedWhileItsTurnIsStillWindingDown(t *testing.T) {
	f := newHostFix(t)
	f.h.L = stubbornLauncher{f.l}
	rv := &fakeRevoker{}
	f.h.ChildTokens = rv
	out := f.task(t, 0, 3)
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if rv.revoked(out.ConversationID) {
		t.Fatal("revoked before any stop")
	}
	if _, err := f.st.Enqueue(Command{ProfileID: "p", ConversationID: out.ConversationID, Kind: KindStop, TurnID: "stop-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.Tick(); err != nil {
		t.Fatal(err)
	}
	if !rv.revoked(out.ConversationID) {
		t.Fatalf("a stop did not revoke the child's token while its turn still runs: %v", rv.calls)
	}
	f.finish()
}

func TestARowThatEndsRevokesTheTokensAndAFailedRevokeHoldsTheChargeBack(t *testing.T) {
	f := newHostFix(t)
	rv := &fakeRevoker{err: fmt.Errorf("db busy")}
	f.h.ChildTokens = rv
	conv := f.endedChild(t, 0, 1)
	f.h.missionChildPass()
	if !rv.revoked(conv) {
		t.Fatal("the ended row did not revoke the child's tokens")
	}
	if f.missionSpent(t) != 0 {
		t.Fatal("charged while the child's token could not be revoked")
	}
	rv.mu.Lock()
	rv.err = nil
	rv.mu.Unlock()
	f.h.missionChildPass() // the sweep retries
	if f.missionSpent(t) != 1 {
		t.Fatalf("spent = %v after the revoke worked, want 1", f.missionSpent(t))
	}
}

// ---- which task a conversation is bound to ----

func TestChildTaskOfNamesTheRowsTaskAndRefusesWhatIsOver(t *testing.T) {
	f := newHostFix(t)
	out := f.task(t, 0, 3)
	got, ok, err := f.st.ChildTaskOf("p", out.ConversationID)
	if err != nil || !ok || got.SessionID != f.se.ID || got.TaskID != f.ids[0] || got.Generation == 0 {
		t.Fatalf("ChildTaskOf = %+v %v %v", got, ok, err)
	}
	if cur, err := f.st.ChildAttemptCurrent("p", out.ConversationID, got); err != nil || !cur {
		t.Fatalf("current = %v %v", cur, err)
	}
	if _, ok, err := f.st.ChildTaskOf("p", f.lead.ConversationID); ok || err != nil {
		t.Fatalf("the lead is no child: %v %v", ok, err)
	}
	if _, ok, err := f.st.ChildTaskOf("other", out.ConversationID); ok || err != nil {
		t.Fatalf("another profile reads the row: %v %v", ok, err)
	}
	// a mission that is no longer running: a refusal, not "an ordinary session"
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'stopped' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.st.ChildTaskOf("p", out.ConversationID); !ok || err != ErrChildNotRunnable {
		t.Fatalf("a stopped mission's child: %v %v, want ErrChildNotRunnable", ok, err)
	}
	// and an ended row is neither: it gets no token, and the old attempt is no longer current
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET state = 'running' WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.End("p", out.ConversationID, EndDone); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := f.st.ChildTaskOf("p", out.ConversationID); ok || err != nil {
		t.Fatalf("an ended child is still a child: %v %v", ok, err)
	}
	if cur, _ := f.st.ChildAttemptCurrent("p", out.ConversationID, got); cur {
		t.Fatal("an ended attempt is current")
	}
}

func TestAReservationStillSpawningCountsAsTheTasksChild(t *testing.T) {
	f := newHostFix(t)
	out := f.task(t, 0, 3)
	// the host can pick up the first message before the lead links the row: the row is still spawning
	if _, err := f.st.db.Exec(`UPDATE coder_mission_children SET state = 'spawning', child_conversation_id = '', pending_conversation_id = ? WHERE session_id = ?`, out.ConversationID, f.se.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := f.st.ChildTaskOf("p", out.ConversationID); err != nil || !ok || got.TaskID != f.ids[0] {
		t.Fatalf("a spawning row: %+v %v %v", got, ok, err)
	}
}

// Once an ended child's spend is on the mission's meter, the cap that was lowered to that spend must not count as
// held a second time, or a mission would refuse new children at half its budget.
func TestACarveIsNotCountedTwiceOnceTheChildsSpendIsOnTheMeter(t *testing.T) {
	f := newHostFix(t)
	if _, err := f.st.db.Exec(`UPDATE task_sessions SET budget_usd = 10 WHERE id = ?`, f.se.ID); err != nil {
		t.Fatal(err)
	}
	f.endedChild(t, 0, 4)
	f.h.missionChildPass()
	if got := f.missionSpent(t); got != 4 {
		t.Fatalf("spent = %v", got)
	}
	// $6 is left and nothing runs: a $5.50 carve fits (it would not if the ended child's $4 were counted again)
	if _, err := f.st.Mark(f.scope(), "p", f.se.ID, f.ids[1], MarkOptions{BudgetUSD: 5.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Reserve(f.scope(), "p", f.se.ID, f.ids[1]); err != nil {
		t.Fatalf("a carve that fits what the mission has left was refused: %v", err)
	}
}
