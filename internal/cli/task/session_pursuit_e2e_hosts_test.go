package task

import (
	"fmt"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// addMission begins a second mission, in the chat conv, through the same real verbs.
func (w *e2eWorld) addMission(conv string, limits ...string) int64 {
	w.t.Helper()
	mintSessionToken(w.t, w.db, "default", conv)
	w.sql(`UPDATE coder_sessions SET chat_mode = 'mission', budget_usd = 5, state = 'idle', turn_id = '', turn_pid = 0, turn_stamp = '' WHERE conversation_id = ?`, conv)
	w.sql(`UPDATE ai_chat_conversations SET runtime_id = 'claude', model = 'opus' WHERE id = ?`, conv)
	s := sessionNew(w.t, w.db, "Second mission", "--kind", "mission", "--conversation", conv)
	w.sql(`UPDATE task_sessions SET budget_usd = 20 WHERE id = ?`, s)
	mustTaskJSON(w.t, w.db, "default", &goalDoc{}, goalJSON, "session", "goal", "set", id(s), "--criteria", "-")
	var p previewDoc
	mustTaskJSON(w.t, w.db, "default", &p, "", append([]string{"session", "pursuit", "preview", id(s)}, limits...)...)
	var begun pursuitDoc
	mustTaskJSON(w.t, w.db, "default", &begun, "", append([]string{"session", "pursuit", "begin", id(s), "--goal-hash", p.Goal.Hash, "--consent", p.Digest, "--chat", p.ChatID}, limits...)...)
	if !begun.Pursuit.Enabled {
		w.t.Fatalf("begin: %+v", begun.Pursuit)
	}
	w.sql(`UPDATE mission_pursuit SET updated_at = ? WHERE session_id = ?`, agedStamp(), s)
	sessionAdd(w.t, w.db, s, "the work")
	mustTaskJSON(w.t, w.db, "default", &sessionDoc{}, "", "session", "approve", id(s))
	aged := agedStamp()
	w.sql(`UPDATE task_session_events SET at = ? WHERE session_id = ? AND kind = 'approved'`, aged, s)
	w.sql(`UPDATE task_sessions SET approved_at = ? WHERE id = ?`, aged, s)
	return s
}

func (w *e2eWorld) epochsOf(s int64) int {
	var n int
	if err := w.d.DB.QueryRow(`SELECT COUNT(*) FROM mission_strategy_epochs WHERE session_id = ?`, s).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// roundsLeavingTheHour is rounds, and after each round moves what the given statement says out of the host's hour: the world
// does not wait an hour, it ages the rows of the cap that is not under test (the cap windows are the host's real clock).
func (w *e2eWorld) roundsLeavingTheHour(n int, leave string) {
	w.t.Helper()
	w.deps.MaxEpochsPerHour = 12
	w.pass()
	for i := 0; i < n; i++ {
		w.advance(31 * time.Minute)
		w.pass()
		w.tick()
		w.finishTurns()
		w.sql(leave, time.Now().UTC().Add(-3*time.Hour).Format(time.RFC3339Nano))
	}
}

const (
	leaveWakes = `UPDATE coder_session_commands SET created_at = ? WHERE substr(turn_id, 1, 8) = 'pursuit-'`
)

// Two missions begun through the verbs share the host's twelve judge calls an hour: each gets its share, the thirteenth call of
// the hour is not made for either, and the status of each says how many of the twelve the host has used. (The wake-ups are aged
// out of the hour each round, so that this cap alone binds.)
func TestTwoCLIBegunMissionsShareTheHostsTwelveJudgeCallsAnHour(t *testing.T) {
	w := newE2EWorld(t, "--wakes-max", "12")
	second := w.addMission("chat2", "--wakes-max", "12")
	w.sql(`UPDATE task_sessions SET budget_usd = 1000`) // the wake turns are charged their cap: the 20% rule must not bind here
	w.sql(`UPDATE coder_sessions SET budget_usd = 1000`)
	w.roundsLeavingTheHour(coderhost.MaxPursuitJudgeCallsPerHour/2, leaveWakes)
	if w.model.count() != coderhost.MaxPursuitJudgeCallsPerHour || w.epochsOf(w.s) != 6 || w.epochsOf(second) != 6 {
		t.Fatalf("%d judge calls, epochs %d and %d: each mission should have had six; %+v", w.model.count(), w.epochsOf(w.s), w.epochsOf(second), w.status())
	}
	w.roundsLeavingTheHour(2, leaveWakes) // the next calls of the same hour are not made: the host-wide cap is used up
	if w.model.count() != coderhost.MaxPursuitJudgeCallsPerHour {
		t.Fatalf("%d judge calls: the thirteenth call of the hour was made", w.model.count())
	}
	if p := w.status(); p.HostJudges != coderhost.MaxPursuitJudgeCallsPerHour || p.HostJudgesMax != coderhost.MaxPursuitJudgeCallsPerHour {
		t.Fatalf("the status does not show the host-wide use: %+v", p)
	}
}

// The same for the twelve wake-ups an hour: six were queued for other missions already; the missions get three more each, the
// thirteenth wake-up of the hour is not queued for either, and no judge call is paid for a wake-up that could not be delivered.
// (Only six judge calls were made, so that this cap alone binds.)
func TestTwoCLIBegunMissionsShareTheHostsTwelveWakeUpsAnHour(t *testing.T) {
	w := newE2EWorld(t, "--wakes-max", "12")
	second := w.addMission("chat2", "--wakes-max", "12")
	w.sql(`UPDATE task_sessions SET budget_usd = 1000`)
	w.sql(`UPDATE coder_sessions SET budget_usd = 1000`)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < coderhost.MaxPursuitWakesPerHour/2; i++ { // wake-ups of other missions, queued in this hour
		w.sql(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, created_at, updated_at, issuer) VALUES ('default', 'other', 'send', ?, 'x', '[]', 'done', ?, ?, '')`,
			fmt.Sprintf("pursuit-9-%d", i), now, now)
	}
	w.rounds(3)
	if w.wakeCount() != 6 || w.epochsOf(w.s) != 3 || w.epochsOf(second) != 3 {
		t.Fatalf("%d wake turns, epochs %d and %d: each mission should have had three", w.wakeCount(), w.epochsOf(w.s), w.epochsOf(second))
	}
	calls := w.model.count()
	w.rounds(2)
	if w.wakeCount() != 6 || w.model.count() != calls {
		t.Fatalf("%d wake turns, %d judge calls (was %d): the thirteenth wake-up of the hour was queued, or paid for", w.wakeCount(), w.model.count(), calls)
	}
	if p := w.status(); p.HostWakes != coderhost.MaxPursuitWakesPerHour || p.HostWakesMax != coderhost.MaxPursuitWakesPerHour {
		t.Fatalf("the status does not show the host-wide use: %+v", p)
	}
}

// begin refuses a consent the person read before what they read changed, writes nothing, and the loop stays silent on that
// mission; a fresh preview begins it and the loop then runs.
func TestAStaleConsentBeginsNothingAndTheLoopStaysSilentUntilAFreshOneBegins(t *testing.T) {
	w := wireE2E(t, newBeginWorld(t))
	p := w.preview(t)
	w.host(`{"pid":1,"pursuit":{"enabled":true,"pin":"claude/haiku@4","wakeCapUsd":5}}`) // the wake-up cap moved after the person read it
	w.refused(t, 3, "consent_stale", w.beginArgs(p)...)
	w.rounds(3)
	if w.model.count() != 0 || len(w.epochs()) != 0 || w.wakeCount() != 0 {
		t.Fatalf("a mission whose begin was refused was pursued: %d judge calls, epochs %v, %d wake turns", w.model.count(), w.epochs(), w.wakeCount())
	}
	w.begin()
	w.rounds(1)
	if w.model.count() != 1 || len(w.epochs()) != 1 {
		t.Fatalf("after a fresh consent: %d judge calls, epochs %v", w.model.count(), w.epochs())
	}
}
