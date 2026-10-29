package ai

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Answers to a dynamic-org worker's questions (#256). `chat turn answer`
// (another process) records the user's answer here; the running turn's
// conductor takes it and journals it, so the turn process stays the only
// writer of its own journal.

// ErrAlreadyAnswered is returned for a second answer to the same question.
var ErrAlreadyAnswered = errors.New("that question already has an answer")

// AddAnswer records the user's answer to a worker's question.
func (s *AIStore) AddAnswer(profileID, conversationID, turnID, agentID, questionID, text string) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	// One statement that holds only while the conversation exists, so an answer racing a delete
	// either lands before it (and goes with it) or is refused, never left behind.
	res, err := s.db.Exec(`INSERT INTO ai_chat_answers (profile_id, conversation_id, turn_id, agent_id, question_id, text, created_at)
		SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM ai_chat_conversations WHERE id = ? AND profile_id = ?)`,
		profileID, conversationID, turnID, agentID, questionID, text, nowRFC3339(), conversationID, profileID)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrAlreadyAnswered
	}
	if err != nil {
		return fmt.Errorf("add answer: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConversationNotFound
	}
	return nil
}

// TakeAnswer returns the answer to a question once, marking it consumed.
func (s *AIStore) TakeAnswer(profileID, turnID, agentID, questionID string) (string, bool, error) {
	if profileID == "" {
		return "", false, ErrProfileRequired
	}
	var id int64
	var text string
	err := s.db.QueryRow(`SELECT id, text FROM ai_chat_answers
		WHERE profile_id = ? AND turn_id = ? AND agent_id = ? AND question_id = ? AND consumed_at = ''`,
		profileID, turnID, agentID, questionID).Scan(&id, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("take answer: %w", err)
	}
	res, err := s.db.Exec(`UPDATE ai_chat_answers SET consumed_at = ? WHERE id = ? AND consumed_at = ''`, nowRFC3339(), id)
	if err != nil {
		return "", false, fmt.Errorf("take answer: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", false, nil // taken by someone else meanwhile
	}
	return text, true, nil
}
