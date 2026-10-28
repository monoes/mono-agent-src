package coderhost

import (
	"errors"
	"math"
)

// Who owns a pause. A session is paused by a command, and the command history says by whom: a person's pause
// (operator CLI or GUI) has no issuer and a turn id that is not the pass's own (cascadeTurn); a master's has an
// issuer; the mission cascade's has cascadeTurn. Nothing else pauses a session, so a kill switch, a budget halt or
// an operator's pause is a no-issuer command and counts as a person's.
//
// The rule: a MODEL path (a resume with an issuer, which is what session_resume queues, or the cascade's own
// resume) never lifts a pause a person made. A person's resume does. The history is the record (no column, no
// migration): the pause is the person's while a person's pause command is done, or still waiting ahead of the
// resume, and no person's resume has been done since. A session leaves "paused" only through a resume, so the
// history cannot go stale. The rule is enforced where the resume is APPLIED, in the one statement that lifts the
// pause, and again when the cascade enqueues its resume, so a person's pause that lands in between wins.

// ErrPersonPause is why a model's resume was refused.
var ErrPersonPause = errors.New("the pause was made by a person; only a person's resume lifts it")

// modelResume says whether the resume came from a model path: a master's (an issuer) or the cascade's own.
func modelResume(c Command) bool { return c.Issuer != "" || c.TurnID == cascadeTurn }

// personPauseLive is a SQL condition (args: profile, conversation, cascadeTurn, id bound, id bound, cascadeTurn,
// profile, conversation) true while a person's pause that was queued before the resume with the bound id is
// standing: not undone by a person's resume done after it.
const personPauseLive = `EXISTS (SELECT 1 FROM coder_session_commands p WHERE p.profile_id = ? AND p.conversation_id = ?
	AND p.kind = 'pause' AND p.issuer = '' AND p.turn_id <> ? AND p.id < ? AND (p.status = 'done' OR p.status = 'pending')
	AND NOT EXISTS (SELECT 1 FROM coder_session_commands r WHERE r.profile_id = p.profile_id AND r.conversation_id = p.conversation_id
		AND r.kind = 'resume' AND r.status = 'done' AND r.issuer = '' AND r.turn_id <> ? AND r.id > p.id AND r.id < ?))`

// liftPause moves a paused session to next, in one statement, unless the resume c is a model's and a person's
// pause stands. It says whether the session is no longer paused; false means the pause was kept for a person.
func (s *Store) liftPause(c Command, next State) (bool, error) {
	bound := int64(math.MaxInt64)
	if c.ID > 0 {
		bound = c.ID
	}
	q := `UPDATE coder_sessions SET state = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ? AND state = 'paused'`
	args := []any{string(next), s.stamp(), c.ProfileID, c.ConversationID}
	if modelResume(c) {
		q += ` AND NOT ` + personPauseLive
		args = append(args, c.ProfileID, c.ConversationID, cascadeTurn, bound, cascadeTurn, bound)
	}
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return true, nil
	}
	x, ok, err := s.GetSession(c.ProfileID, c.ConversationID)
	if err != nil {
		return false, err
	}
	return !ok || x.State != StatePaused, nil // already lifted by someone else counts as lifted
}

// enqueueCascadeResume queues the pass's own resume unless a person's pause stands, decided by the INSERT itself
// so a person's pause queued after the pass looked is not followed by a resume queued behind its back.
func (s *Store) enqueueCascadeResume(profileID, conv string) (bool, error) {
	now := s.stamp()
	res, err := s.db.Exec(`INSERT INTO coder_session_commands (profile_id, conversation_id, kind, turn_id, message, attachments, status, wants_mission, created_at, updated_at, issuer)
		SELECT ?, ?, 'resume', ?, '', '[]', 'pending', 0, ?, ?, '' WHERE NOT `+personPauseLive,
		profileID, conv, cascadeTurn, now, now, profileID, conv, cascadeTurn, int64(math.MaxInt64), cascadeTurn, int64(math.MaxInt64))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

