package coderhost

import (
	"testing"
	"time"
)

// answerAs marks the oldest open question answered by who, now.
func (w *pworld) answerAs(who string) {
	w.exec(`UPDATE coder_session_questions SET status = 'answered', answer = 'x', answered_by = ?, answered_at = ? WHERE id = (SELECT MIN(id) FROM coder_session_questions WHERE status = 'open')`,
		who, w.clk.Now().Format(time.RFC3339Nano))
}

func TestTheParkedQuestionLimitStopsNewEpochsAndTheStateSaysWaiting(t *testing.T) {
	w := newPursuitWorld(t)
	w.pass()
	w.advance(31 * time.Minute)
	w.park(2)
	w.pass()
	if got := w.epochs(); len(got) != 1 {
		t.Fatalf("below the limit (2 of 3) the pursuit must go on: %v", got)
	}
	if w.waiting() != "" {
		t.Fatalf("waiting = %q below the limit", w.waiting())
	}
	w.advance(31 * time.Minute)
	w.park(1) // the third parked question
	w.pass()
	if got := w.epochs(); len(got) != 1 || w.model.count() != 1 {
		t.Fatalf("an epoch opened at the limit: %v", got)
	}
	if w.waiting() != "parked" {
		t.Fatalf("waiting = %q, want parked", w.waiting())
	}
	wakes := len(w.wakes())
	w.advance(5 * time.Hour)
	w.pass()
	if len(w.epochs()) != 1 || len(w.wakes()) != wakes {
		t.Fatal("a waiting pursuit went on")
	}
}

func TestTheSpendLimitStopsNewEpochs(t *testing.T) {
	w := newPursuitWorld(t)
	w.pass()
	w.advance(31 * time.Minute)
	w.setSpent(1.99) // 19.9% of 10 USD since the person acted
	w.pass()
	if len(w.epochs()) != 1 || w.waiting() != "" {
		t.Fatalf("below 20%%: epochs %v waiting %q", w.epochs(), w.waiting())
	}
	w.advance(31 * time.Minute)
	w.setSpent(2.0)
	w.pass()
	if len(w.epochs()) != 1 || w.waiting() != "spend" {
		t.Fatalf("at 20%%: epochs %v waiting %q", w.epochs(), w.waiting())
	}
}

func TestSpendBeforeThePersonLastActedDoesNotCount(t *testing.T) {
	w := newPursuitWorld(t)
	w.setSpent(5) // half the budget, spent before the pursuit was first seen: the baseline
	w.pass()
	w.advance(31 * time.Minute)
	w.setSpent(6.5)
	w.pass()
	if len(w.epochs()) != 1 || w.waiting() != "" {
		t.Fatalf("epochs %v waiting %q: only the 1.5 USD since the baseline counts", w.epochs(), w.waiting())
	}
}

func TestAnUnknownBudgetIsNotUnlimited(t *testing.T) {
	for _, usd := range []float64{0, -1} {
		w := newPursuitWorld(t)
		w.exec(`UPDATE task_sessions SET budget_usd = ? WHERE id = 77`, usd)
		w.pass()
		w.advance(31 * time.Minute)
		w.pass()
		if len(w.epochs()) != 0 || w.model.count() != 0 || w.waiting() != "budget-unknown" {
			t.Fatalf("budget %v: epochs %v calls %d waiting %q", usd, w.epochs(), w.model.count(), w.waiting())
		}
	}
}

func TestAPersonActionResetsTheCounters(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(w *pworld)
	}{
		{"the person answers a question", func(w *pworld) { w.answerAs(WhoUser) }},
		{"the person resumes the pursuit", func(w *pworld) { w.pause(); w.advance(time.Second); w.resume() }},
		{"the person writes to the lead", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', 'turn-9', 'hello', '[]', 'done', ?, ?, '')`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.pass()
			w.park(3)
			w.advance(31 * time.Minute)
			w.pass()
			if w.waiting() != "parked" || len(w.epochs()) != 0 {
				t.Fatalf("not waiting: %q %v", w.waiting(), w.epochs())
			}
			w.advance(time.Minute)
			c.act(w)
			w.advance(time.Second)
			w.pass()
			if w.waiting() != "" || len(w.epochs()) != 1 {
				t.Fatalf("after the person acted: waiting %q epochs %v", w.waiting(), w.epochs())
			}
		})
	}
}

func TestOnlyTheHostsOwnRecordsCountAsAPersonAction(t *testing.T) {
	for _, c := range []struct {
		name string
		act  func(w *pworld)
	}{
		{"a master answers", func(w *pworld) { w.answerAs("master:k1") }},
		{"the model answers", func(w *pworld) { w.answerAs(WhoModel) }},
		{"an agent sends with its issuer", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', 'turn-9', 'hello', '[]', 'done', ?, ?, 'k1')`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
		{"a host-made wake-up", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', 'pursuit-abc', 'hello', '[]', 'done', ?, ?, '')`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
		{"a host-made check-in", func(w *pworld) {
			w.exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('p', 'm', 'send', 'checkin-abc', 'hello', '[]', 'done', ?, ?, '')`,
				w.clk.Now().Format(time.RFC3339Nano), w.clk.Now().Format(time.RFC3339Nano))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newPursuitWorld(t)
			w.pass()
			w.park(4) // one of them may be settled by someone else: three still wait
			w.advance(31 * time.Minute)
			w.pass()
			w.advance(time.Minute)
			c.act(w)
			w.advance(time.Second)
			w.pass()
			if w.waiting() != "parked" || len(w.epochs()) != 0 {
				t.Fatalf("an agent's or the host's own record reset the counters: waiting %q epochs %v", w.waiting(), w.epochs())
			}
		})
	}
}

func TestAnAnsweredQuestionDoesNotCountAsParked(t *testing.T) {
	w := newPursuitWorld(t)
	w.pass()
	w.park(3)
	w.answerAs(WhoModel) // settled by the model's default, not by the person: no longer waiting for them
	w.advance(31 * time.Minute)
	w.pass()
	if w.waiting() != "" || len(w.epochs()) != 1 {
		t.Fatalf("waiting %q epochs %v: two parked questions are below the limit of 3", w.waiting(), w.epochs())
	}
}
