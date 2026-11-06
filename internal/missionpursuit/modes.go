package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// ChatModeMission is the chat_mode of a mission chat (coder_sessions.chat_mode, migration 163).
const ChatModeMission = "mission"

// A ModeReader says the chat mode of a coder conversation. It reads through x, the connection of the transaction that
// writes the goal, so the answer holds until it commits (a mode is locked at the first message anyway).
type ModeReader interface {
	ChatMode(ctx context.Context, x tasks.Queryer, profileID, conversationID string) (string, error)
}

// CoderSessionModes is the real reader: the chat_mode column of coder_sessions. A conversation with no row has no mode.
type CoderSessionModes struct{}

func (CoderSessionModes) ChatMode(ctx context.Context, x tasks.Queryer, profileID, conversationID string) (string, error) {
	var mode string
	err := x.QueryRowContext(ctx, `SELECT chat_mode FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("missionpursuit: reading the chat mode: %w", err)
	}
	return mode, nil
}
