package coderhost

import (
	"fmt"
	"strings"
	"time"
)

// What counts as the person acting, for the away limits of a pursuit. Only the host's own records count, never a claim
// an agent makes:
//   - the pursuit row moved (the person started, paused or resumed it: only the operator's store calls write it),
//   - a question of the mission's chats was answered BY THE PERSON (answered_by = user; a master's or the model's answer
//     is not),
//   - an ask was answered by the person (answered_by = person), or the operator approved the session (the event kind
//     "approved", written only by the operator's CLI path, and the approval stamp on the session),
//   - a message reached the lead's chats with no issuer (an agent's send carries its issuer) and either a turn id that is
//     not a host-made one, or the person's own answer notes (the reply to a plan or to an ask: answer-p-, ask-p-, queued
//     only by the operator's surfaces).
//
// NOT the operator's label in the session history as such: the label "you" is that of every Human actor, and the HOST
// writes under it too (the charge of an ended child, an in-turn worker's spend, the conductor's close proposal, a task
// added). Only the event kinds listed in personEventKinds, which a person alone can cause, count.
//
// The newest 50 answers and 50 messages are read: more host-made sends than that after a person's message, before the
// loop first sees it (a long outage), hide it; once seen it is stored and cannot regress.
// A time that does not read counts for nothing here (it can never reset the counters), and a question time that does not
// read counts as parked (it can only make the limit tighter).

const maxTreeConversations = 200

// personEventKinds are the session event kinds that only a person causes: the operator's approval of the plan. Never add a
// kind that host automation also writes under the operator's label (spend, close_proposed, task_added, created, limits...).
var personEventKinds = []any{"approved"}

// personActorLabel is how the task store writes an action of the operator (tasks.Actor{Kind: Human}.Label()).
const personActorLabel = "you"

// missionTree is the lead's conversation and the conversations below it (children, grandchildren, ...), bounded.
func (s *Store) missionTree(m pursuitMission) ([]string, error) {
	if m.Conv == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`WITH RECURSIVE t(c, d) AS (SELECT ?, 0 UNION SELECT s.conversation_id, t.d + 1 FROM coder_sessions s JOIN t ON s.parent_conversation_id = t.c WHERE s.profile_id = ? AND t.d < 4)
		SELECT c FROM t LIMIT ?`, m.Conv, m.Profile, maxTreeConversations)
	if err != nil {
		return nil, fmt.Errorf("reading the mission's chats: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// personActionAt is the latest action of the person the host's records show (the zero time when none).
func (s *Store) personActionAt(m pursuitMission, tree []string) (time.Time, error) {
	var best time.Time
	take := func(v string) {
		// The pursuit row is stamped in whole seconds, the answers and messages in nanoseconds: all are compared in whole
		// seconds. A clock stepped back makes a later action look older than the one already seen; it is then not noticed
		// and the limits stay on (fail closed).
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil && t.Truncate(time.Second).After(best) {
			best = t.Truncate(time.Second)
		}
	}
	var moved string
	if err := s.db.QueryRow(`SELECT updated_at FROM mission_pursuit WHERE session_id = ?`, m.ID).Scan(&moved); err != nil {
		return time.Time{}, fmt.Errorf("reading the pursuit row: %w", err)
	}
	take(moved)
	if len(tree) == 0 {
		return best, nil
	}
	args := []any{m.Profile, WhoUser}
	for _, c := range tree {
		args = append(args, c)
	}
	rows, err := s.db.Query(`SELECT answered_at FROM coder_session_questions WHERE profile_id = ? AND status = 'answered' AND answered_by = ? AND conversation_id IN (`+placeholders(len(tree))+`) ORDER BY id DESC LIMIT 50`, args...)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the answers: %w", err)
	}
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		take(at)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return time.Time{}, err
	}
	// an ask (a critical question to the person) the person answered
	args = []any{m.Profile}
	for _, c := range tree {
		args = append(args, c)
	}
	rows, err = s.db.Query(`SELECT answered_at FROM coder_session_asks WHERE profile_id = ? AND status = 'answered' AND answered_by = 'person' AND asker_conv IN (`+placeholders(len(tree))+`) ORDER BY id DESC LIMIT 50`, args...)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the answered asks: %w", err)
	}
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			rows.Close()
			return time.Time{}, err
		}
		take(at)
	}
	rows.Close()
	// the mission session's own history: what the person did to it (a plan answered or approved, a task closed, a limit
	// changed) is written with the operator's label; approval stamps the session itself
	evs, err := s.db.Query(`SELECT at FROM task_session_events WHERE session_id = ? AND actor = ? AND kind IN (`+placeholders(len(personEventKinds))+`) ORDER BY id DESC LIMIT 50`,
		append([]any{m.ID, personActorLabel}, personEventKinds...)...)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the session history: %w", err)
	}
	for evs.Next() {
		var at string
		if err := evs.Scan(&at); err != nil {
			evs.Close()
			return time.Time{}, err
		}
		take(at)
	}
	evs.Close()
	var approved string
	_ = s.db.QueryRow(`SELECT approved_at FROM task_sessions WHERE id = ? AND approved_by = ?`, m.ID, personActorLabel).Scan(&approved)
	take(approved)
	args = []any{m.Profile}
	for _, c := range tree {
		args = append(args, c)
	}
	rows, err = s.db.Query(`SELECT turn_id, created_at FROM coder_session_commands WHERE profile_id = ? AND kind = 'send' AND issuer = '' AND conversation_id IN (`+placeholders(len(tree))+`) ORDER BY id DESC LIMIT 50`, args...)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading the messages: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var turn, at string
		if err := rows.Scan(&turn, &at); err != nil {
			return time.Time{}, err
		}
		if !HostMade(turn) || strings.HasPrefix(turn, AnswerPrefix+answerFromPerson+"-") || strings.HasPrefix(turn, AskPrefix+askFromPerson+"-") {
			take(at)
		}
	}
	return best, rows.Err()
}

// parkedSince counts the questions of the mission's chats that were parked after since and still wait for an answer.
func (s *Store) parkedSince(m pursuitMission, tree []string, since time.Time) (int, error) {
	if len(tree) == 0 {
		return 0, nil
	}
	args := []any{m.Profile}
	for _, c := range tree {
		args = append(args, c)
	}
	rows, err := s.db.Query(`SELECT created_at FROM coder_session_questions WHERE profile_id = ? AND status = 'open' AND conversation_id IN (`+placeholders(len(tree))+`) LIMIT 1000`, args...)
	if err != nil {
		return 0, fmt.Errorf("reading the parked questions: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, err
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err != nil || t.Truncate(time.Second).After(since) {
			n++
		}
	}
	return n, rows.Err()
}
