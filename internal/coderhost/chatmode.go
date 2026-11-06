package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The chat mode of a coder conversation: 'off', 'mission' or 'open' (open-ended). It is chosen before the first
// message and never changes afterwards: the column coder_sessions.chat_mode holds it and a trigger (migration 163)
// refuses a change once the conversation has a message. This file is the only place that reads or writes the column.
const (
	ChatModeOff     = "off"
	ChatModeMission = "mission"
	ChatModeOpen    = "open"
)

// ChatModes are the values the column allows.
var ChatModes = []string{ChatModeOff, ChatModeMission, ChatModeOpen}

// ValidChatMode says whether m is one of the three modes.
func ValidChatMode(m string) bool { return oneOf(m, ChatModes) }

// OpenModeAvailability says whether the open-ended mode can be chosen and, when it cannot, why. It is the one
// place the answer comes from; nothing starts an open-ended run from a chat yet, so for now it is never available.
var OpenModeAvailability = func() (ok bool, reason string) { return false, "not available yet" }

// ErrChatModeLocked is what a change of the mode answers once the conversation has a message.
var ErrChatModeLocked = errors.New("the chat mode is chosen before the first message and cannot change afterwards")

// ChatModeRow reads the one-row queries of both a connection and a transaction.
type ChatModeRow interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ChatModeIn reads the conversation's mode through q ('off' when it has no session row yet).
func ChatModeIn(ctx context.Context, q ChatModeRow, profileID, conversationID string) (string, error) {
	var m string
	err := q.QueryRowContext(ctx, `SELECT chat_mode FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatModeOff, nil
	}
	if err != nil {
		return ChatModeOff, fmt.Errorf("coder host: reading the chat mode of %s: %w", conversationID, err)
	}
	return m, nil
}

// ChatMode is the conversation's mode.
func (s *Store) ChatMode(profileID, conversationID string) (string, error) {
	return ChatModeIn(context.Background(), s.db, profileID, conversationID)
}

// SetChatMode is the way to set the mode on its own (a conversation just created): it creates the session row
// if needed. Every other writer changes it through UpdateSettings, which ends in the same write.
func (s *Store) SetChatMode(profileID, conversationID, mode string) error {
	if !ValidChatMode(mode) {
		return fmt.Errorf("the chat mode is %s", strings.Join(ChatModes, ", "))
	}
	_, err := s.UpdateSettings(profileID, conversationID, func(_ context.Context, _ *sql.Conn, cur Settings) (Settings, error) {
		next := cur
		next.ChatMode = mode
		return next, nil
	})
	return err
}

// withChatMode is the settings with the mode as the column has it; the legacy Mission flag follows the mode.
func withChatMode(s Settings, mode string) Settings {
	if !ValidChatMode(mode) {
		mode = ChatModeOff
	}
	s.ChatMode, s.Mission = mode, mode == ChatModeMission
	return s
}

// ResolveChatMode is the mode a change asks for: next.ChatMode, or, when only the legacy Mission flag differs from
// cur, 'mission' (on) or 'off'. A change of both that disagrees is refused.
func ResolveChatMode(cur, next Settings) (string, error) {
	want := next.ChatMode
	if want == "" {
		want = cur.ChatMode
	}
	if !ValidChatMode(want) {
		return "", fmt.Errorf("chat_mode is %s", strings.Join(ChatModes, ", "))
	}
	if next.Mission == cur.Mission {
		return want, nil
	}
	fromFlag := ChatModeOff
	if next.Mission {
		fromFlag = ChatModeMission
	}
	if want != cur.ChatMode && want != fromFlag {
		return "", errors.New("mission and chat_mode disagree: give one of them")
	}
	return fromFlag, nil
}

// writeChatMode is the one write of the column. A mode other than the stored one needs the open mode to be
// available; the trigger of migration 163 refuses it once the conversation has a message.
func writeChatMode(ctx context.Context, conn *sql.Conn, profileID, conversationID, was, mode string) error {
	if mode == was {
		return nil
	}
	if mode == ChatModeOpen {
		if ok, reason := OpenModeAvailability(); !ok {
			return fmt.Errorf("the open-ended chat mode is %s", reason)
		}
	}
	_, err := conn.ExecContext(ctx, `UPDATE coder_sessions SET chat_mode = ? WHERE profile_id = ? AND conversation_id = ?`, mode, profileID, conversationID)
	if err != nil && strings.Contains(err.Error(), "chat_mode is locked") {
		return ErrChatModeLocked
	}
	return err
}

// ChatModeLockedIn says whether the conversation already has a message, so that its mode can no longer change: a send
// waiting for or taken by the host, or a turn. It asks what the trigger of migration 163 asks.
func ChatModeLockedIn(ctx context.Context, q ChatModeRow, profileID, conversationID string) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send')
		OR EXISTS (SELECT 1 FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ?)`, profileID, conversationID, profileID, conversationID).Scan(&n)
	return n != 0, err
}

// SetNewChatModeOn gives a conversation that is being created its mode, on the connection of the transaction that
// inserts it (ai.AIStore.CreateConversationAccessIn): the session row and the mode are written with the conversation
// or not at all. It is the same write as every other change of the mode.
func (s *Store) SetNewChatModeOn(ctx context.Context, conn *sql.Conn, profileID, conversationID, mode string) error {
	if !ValidChatMode(mode) {
		return fmt.Errorf("the chat mode is %s", strings.Join(ChatModes, ", "))
	}
	if err := ensureSessionOn(ctx, conn, profileID, conversationID, s.stamp()); err != nil {
		return err
	}
	return writeChatMode(ctx, conn, profileID, conversationID, ChatModeOff, mode)
}
