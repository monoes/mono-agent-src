package ai

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// NoticeOpenEndedStopped is the code of the system notice a Stop of an open-ended run leaves in a chat.
const NoticeOpenEndedStopped = "doomsday.stopped"

// systemTurnPrefix starts the id of a turn that only carries a system notice.
const systemTurnPrefix = "system-"

// RecordSystemNotice puts a message into a conversation's transcript that no turn of the conversation wrote: a
// finished turn of its own with no prompt, one notice event (info) and the turn.finished that closes it, written in
// one transaction. It is never active, so it is written whatever other turn the conversation has, and it is checked
// against the profile like every lookup here. The frontend shows a promptless turn as its notice alone.
func (s *AIStore) RecordSystemNotice(profileID, conversationID, code, message string) error {
	if profileID == "" {
		return ErrProfileRequired
	}
	if code == "" || strings.TrimSpace(message) == "" {
		return errors.New("chat: a system notice needs a code and a message")
	}
	if _, err := s.GetConversation(conversationID, profileID); err != nil {
		return err
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return fmt.Errorf("chat: system turn id: %w", err)
	}
	turnID := systemTurnPrefix + hex.EncodeToString(rnd[:])
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin system notice: %w", err)
	}
	defer tx.Rollback()
	now := time.Now()
	stamp := now.UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at)
		VALUES (?, ?, ?, '', '', 'completed', '', 0, ?, ?)`, turnID, conversationID, profileID, stamp, stamp); err != nil {
		return fmt.Errorf("create system turn: %w", err)
	}
	if _, err := appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventNotice,
		chatevents.NoticePayload{Code: code, Message: message, Severity: chatevents.SeverityInfo}, now); err != nil {
		return err
	}
	ev, err := appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventTurnFinished,
		chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true}, now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ai_chat_turns SET last_committed_seq = ? WHERE id = ? AND profile_id = ?`, ev.Seq, turnID, profileID); err != nil {
		return fmt.Errorf("advance system turn: %w", err)
	}
	return tx.Commit()
}
