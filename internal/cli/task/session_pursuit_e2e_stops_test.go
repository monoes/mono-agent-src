package task

import (
	"errors"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

// The stops of a pursuit begun by the CLI, each on its own world: the loop stops where its rule says, with the reason the
// person reads in `pursuit status`, and goes on only when the person acts.

// rounds runs n epochs' worth of time: the engine starts, then each round the timer is due, a pass runs, the host takes its
// turn for any wake-up queued and the lead's turn ends.
func (w *e2eWorld) rounds(n int) {
	w.t.Helper()
	w.deps.MaxEpochsPerHour = 12
	w.pass()
	for i := 0; i < n; i++ {
		w.advance(31 * time.Minute)
		w.pass()
		w.tick()
		w.finishTurns()
	}
}

func (w *e2eWorld) waiting() string { w.t.Helper(); return w.status().Waiting }

func (w *e2eWorld) wantStopped(what, waiting string, calls, wakes int) {
	w.t.Helper()
	if got := w.waiting(); got != waiting || w.model.count() != calls || w.wakeCount() != wakes {
		w.t.Fatalf("%s: waiting %q (want %q), judge calls %d (want %d), wake turns %d (want %d)", what, got, waiting, w.model.count(), calls, w.wakeCount(), wakes)
	}
}

func TestTheJudgeFailingThreeTimesStopsACLIBegunPursuitUntilThePersonActs(t *testing.T) {
	w := newE2EWorld(t)
	w.model.failWith = errors.New("transport down")
	w.rounds(missionpursuit.MaxBadJudgeRounds)
	w.wantStopped("three failed rounds", "judge-failing", missionpursuit.MaxBadJudgeRounds, 0)
	w.rounds(3)
	w.wantStopped("rounds after the stop", "judge-failing", missionpursuit.MaxBadJudgeRounds, 0)
	w.personActed("pause")
	w.personActed("resume")
	w.model.failWith = nil
	w.rounds(1)
	if w.model.count() != missionpursuit.MaxBadJudgeRounds+1 || w.waiting() != "" || w.status().BadRounds != 0 {
		t.Fatalf("after the person acted and one good round: %d judge calls, %+v", w.model.count(), w.status())
	}
}

func TestAnUnknownBudgetOrAnUncappedLeadNeverReachesTheJudgeOrWakesTheLead(t *testing.T) {
	for name, tc := range map[string]struct{ q, waiting string }{
		"the mission has no budget": {`UPDATE task_sessions SET budget_usd = 0`, "budget-unknown"},
		"the lead has no cap":       {`UPDATE coder_sessions SET budget_usd = 0 WHERE conversation_id = 'chat1'`, "lead-uncapped"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newE2EWorld(t)
			w.sql(tc.q)
			w.rounds(3)
			w.wantStopped(name, tc.waiting, 0, 0)
			if len(w.epochs()) != 0 || len(w.l.caps) != 0 {
				t.Fatalf("epochs %v, capped starts %v", w.epochs(), w.l.caps)
			}
		})
	}
}

func TestAWakesLimitOfNBegunByTheCLIRunsExactlyNWakeTurns(t *testing.T) {
	w := newE2EWorld(t, "--wakes-max", "2")
	w.rounds(5)
	w.wantStopped("a limit of 2", "wakes", 2, 2)
}

func TestThreeParkedQuestionsStopACLIBegunPursuitUntilThePersonAnswersOne(t *testing.T) {
	w := newE2EWorld(t)
	w.park(3)
	w.rounds(2)
	w.wantStopped("three parked", "parked", 0, 0)
	// the person answers one: two are parked now, below the limit, and what they did resets the count
	w.sql(`UPDATE coder_session_questions SET status = 'answered', answer = 'x', answered_by = ?, answered_at = ? WHERE id = (SELECT MIN(id) FROM coder_session_questions WHERE status = 'open')`,
		coderhost.WhoUser, time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano))
	w.rounds(1)
	if w.waiting() != "" || w.model.count() != 1 {
		t.Fatalf("after the person answered: %+v, %d judge calls", w.status(), w.model.count())
	}
}

func TestAHostWithTheLoopOffStartsNoPursuitWakeQueuedOrForged(t *testing.T) {
	w := newE2EWorld(t)
	w.nextEpoch()
	if len(w.epochs()) != 1 || w.model.count() != 1 {
		t.Fatalf("setup: %v, %d judge calls", w.epochs(), w.model.count())
	}
	// the host restarts without MONOAGENT_PURSUIT_JUDGE: the wake-up the loop queued does not run, and a forged one does not either
	off := &coderhost.Host{Store: w.st, L: w.l, Logf: t.Logf}
	_, _ = w.st.Enqueue(coderhost.Command{ProfileID: "default", ConversationID: "chat1", Kind: coderhost.KindSend, TurnID: coderhost.PursuitTurnPrefix + "forged", Message: "x"})
	if err := off.Tick(); err != nil {
		t.Fatal(err)
	}
	off.Wait()
	if w.wakeCount() != 0 {
		t.Fatalf("a host without the loop started %d pursuit wake turn(s)", w.wakeCount())
	}
	var pending int
	if err := w.d.DB.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE status = 'pending' AND substr(turn_id, 1, 8) = 'pursuit-'`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("%d pursuit wake(s) still wait on a host without the loop (%v): they must fail, not wait for a loop that is not there", pending, err)
	}
	// the control: the host that has the loop, given the next epoch's wake-up for the same lead, does run it (so the refusal above
	// was the missing loop, not a lead that could not be woken)
	// the wake-up the loop-less host failed waits before it is retried (the store reads the real clock, the world's fake one only moves
	// the pursuit): let that wait be over
	w.sql(`UPDATE coder_session_commands SET updated_at = ? WHERE status = 'failed' AND substr(turn_id, 1, 8) = 'pursuit-'`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano))
	w.advance(31 * time.Minute)
	w.pass()
	w.tick()
	if w.wakeCount() != 1 {
		t.Fatalf("the host with the loop started %d wake turn(s) for the same lead: the control failed", w.wakeCount())
	}
}
