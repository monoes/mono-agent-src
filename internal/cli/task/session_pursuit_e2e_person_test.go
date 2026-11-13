package task

import (
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// What lifts the money stop of a pursuit begun by the CLI: only the host's records of the PERSON acting. Every row the host
// or an agent writes (under the operator's label too) is stamped now, after the person's last action, and must change
// nothing; a person's record is stamped before now (aged, as every person action here) and lifts it.

// moneyStopped is a world whose first epoch the 20% rule already refuses: no judge call, no wake.
func moneyStopped(t *testing.T) *e2eWorld {
	t.Helper()
	w := newE2EWorld(t)
	w.pass() // the loop first looks: what was spent before that is not "since the person acted"
	w.setSpent(4.5)
	w.rounds(2)
	w.wantStopped("the money stop", "spend", 0, 0)
	return w
}

func (w *e2eWorld) stillStopped(what string) {
	w.t.Helper()
	w.rounds(1)
	w.wantStopped(what, "spend", 0, 0)
}

func TestHostAndAgentRecordsDoNotLiftTheMoneyStopOfACLIBegunPursuit(t *testing.T) {
	w := moneyStopped(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// the host's own history rows under the operator's label: spend, a close proposal, a task added, the session created
	for _, kind := range []string{"spend", "close_proposed", "task_added", "created", "limits"} {
		w.sql(`INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?, ?, 'you', ?, 'x')`, w.s, now, kind)
		w.stillStopped("a " + kind + " row under the operator's label")
	}
	// host-made sends with no issuer (a pursuit wake-up, a check-in) and sends of an agent (an issuer)
	for i, turn := range []string{"pursuit-1-1", "checkin-1", "master-1"} {
		w.sql(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('default', 'chat1', 'send', ?, 'x', '[]', 'done', ?, ?, '')`, turn, now, now)
		w.stillStopped(fmt.Sprintf("a host-made send %d (%s)", i, turn))
	}
	w.sql(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('default', 'chat1', 'send', 'plain-1', 'x', '[]', 'done', ?, ?, 'some-agent')`, now, now)
	w.stillStopped("a send an agent issued")
	// a model's or a master's answer to a question is not the person answering
	for _, by := range []string{coderhost.WhoModel, coderhost.WhoMaster} {
		w.park(1)
		w.sql(`UPDATE coder_session_questions SET status = 'answered', answer = 'x', answered_by = ?, answered_at = ? WHERE status = 'open'`, by, now)
		w.stillStopped("a question answered by " + by)
	}
	// the pursuit's own view and the operator's reads change nothing either
	w.status()
	w.stillStopped("reading the status")
}

func TestEachOfThePersonsOwnActionsLiftsTheMoneyStopOfACLIBegunPursuit(t *testing.T) {
	aged := func() string { return time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano) }
	for name, act := range map[string]func(w *e2eWorld){
		"the approval of the plan": func(w *e2eWorld) {
			w.sql(`INSERT INTO task_session_events (session_id, at, actor, kind, note) VALUES (?, ?, 'you', 'approved', 'x')`, w.s, aged())
		},
		"an answer of the person to a parked question": func(w *e2eWorld) {
			w.park(1)
			w.sql(`UPDATE coder_session_questions SET status = 'answered', answer = 'x', answered_by = ?, answered_at = ?`, coderhost.WhoUser, aged())
		},
		"a plain message of the person to the lead": func(w *e2eWorld) {
			w.sql(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('default', 'chat1', 'send', 'msg-1', 'hello', '[]', 'done', ?, ?, '')`, aged(), aged())
		},
		"a pause and a resume through the verbs": func(w *e2eWorld) {
			w.personActed("pause")
			w.personActed("resume")
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := moneyStopped(t)
			act(w)
			w.rounds(1)
			if w.waiting() != "" || w.model.count() != 1 {
				t.Fatalf("after %s: %+v, %d judge calls", name, w.status(), w.model.count())
			}
		})
	}
}
