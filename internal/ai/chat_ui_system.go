package ai

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dbconn"
)

// RecordUISurfaces journals system ui.surface events of a conversation as a
// finished turn of their own with no prompt, in one transaction, as
// RecordUIAction does for a press: a running turn's journal has a single
// writer (its process), so a command such as `chat ui sync-hil` never appends
// to it. Every payload must be a system one.
func (s *AIStore) RecordUISurfaces(profileID, conversationID string, ps []chatevents.UISurfacePayload) ([]chatevents.Event, error) {
	if profileID == "" {
		return nil, ErrProfileRequired
	}
	if len(ps) == 0 {
		return nil, nil
	}
	for _, p := range ps {
		if p.Provenance != "system" {
			return nil, fmt.Errorf("chat: RecordUISurfaces takes system surfaces only, got %q for %s", p.Provenance, p.SurfaceID)
		}
	}
	if _, err := s.GetConversation(conversationID, profileID); err != nil {
		return nil, err
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, fmt.Errorf("chat: ui surface turn id: %w", err)
	}
	turnID := UISurfaceTurnPrefix + hex.EncodeToString(rnd[:])
	var out []chatevents.Event
	err := dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error {
		tx := dbconn.ConnTx{Ctx: context.Background(), Conn: conn}
		now := time.Now()
		stamp := now.UTC().Format(time.RFC3339)
		if _, err := tx.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at)
			VALUES (?, ?, ?, '', '', ?, '', 0, ?, ?)`, turnID, conversationID, profileID, string(chatevents.StatusCompleted), stamp, stamp); err != nil {
			return fmt.Errorf("create ui surface turn: %w", err)
		}
		out = out[:0]
		for _, p := range ps {
			ev, err := appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventUISurface, p, now)
			if err != nil {
				return err
			}
			out = append(out, ev)
		}
		fin, err := appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventTurnFinished,
			chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted}, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE ai_chat_turns SET last_committed_seq = ? WHERE id = ? AND profile_id = ?`, fin.Seq, turnID, profileID); err != nil {
			return fmt.Errorf("advance ui surface turn: %w", err)
		}
		return nil
	})
	return out, err
}
