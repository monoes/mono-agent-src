package coderhost

import "time"

// PendingSendLike is the id of a send still waiting in the conversation with exactly this message, queued within the last
// within (0, false when there is none). A caller that must not queue the same text twice (a double click on steering) asks
// first. An unreadable store answers "none": the caller's own bounds still apply.
func (s *Store) PendingSendLike(profileID, conversationID, message string, within time.Duration) (int64, bool) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND status = 'pending'
		AND message = ? AND `+tsInWindow("created_at", s.now().Add(-within))+` ORDER BY id DESC LIMIT 1`, profileID, conversationID, message).Scan(&id)
	return id, err == nil
}
