package ai

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/dbconn"
)

// UIActionTurnPrefix and UISurfaceTurnPrefix start the ids of the turns that
// only carry cards (a button press, surfaces written outside a running turn).
// Both are system turns: promptless, and skipped by readers of the
// conversation's newest or first work turn.
const (
	UIActionTurnPrefix  = systemTurnPrefix + "uiaction-"
	UISurfaceTurnPrefix = systemTurnPrefix + "uisurface-"
)

// cardTurnSQL matches the rows of the card turns (Turn.IsCard).
const cardTurnSQL = `(prompt = '' AND (id LIKE '` + UIActionTurnPrefix + `%' OR id LIKE '` + UISurfaceTurnPrefix + `%'))`

// IsCard says t only carries cards: a button press, or surfaces written outside
// a running turn. It is no turn of the chat: ListTurns leaves it out.
func (t Turn) IsCard() bool {
	return t.Prompt == "" && (strings.HasPrefix(t.ID, UIActionTurnPrefix) || strings.HasPrefix(t.ID, UISurfaceTurnPrefix))
}

// ListCardTurns is ListTurns of the card turns alone, for the readers that draw
// the cards.
func (s *AIStore) ListCardTurns(conversationID, profileID, cursor string, limit int) ([]Turn, string, error) {
	return s.listTurns(conversationID, profileID, cursor, limit, cardTurnSQL)
}

// UIEvents returns every ui.surface and ui.action event of a conversation,
// oldest first across its turns, for rebuilding the live surfaces
// (dynorg.RebuildRegistry).
func (s *AIStore) UIEvents(profileID, conversationID string) ([]chatevents.Event, error) {
	if profileID == "" {
		return nil, ErrProfileRequired
	}
	rows, err := s.db.Query(`SELECT profile_id, conversation_id, turn_id, seq, created_at, version, type, payload
		FROM ai_chat_events WHERE profile_id = ? AND conversation_id = ? AND type IN (?, ?) ORDER BY rowid ASC`,
		profileID, conversationID, string(chatevents.EventUISurface), string(chatevents.EventUIAction))
	if err != nil {
		return nil, fmt.Errorf("ui events: %w", err)
	}
	defer rows.Close()
	var out []chatevents.Event
	for rows.Next() {
		var ev chatevents.Event
		var typ, payload string
		if err := rows.Scan(&ev.ProfileID, &ev.ConversationID, &ev.TurnID, &ev.Seq, &ev.At, &ev.Version, &typ, &payload); err != nil {
			return nil, fmt.Errorf("scan ui event: %w", err)
		}
		ev.Type, ev.Payload = chatevents.EventType(typ), []byte(payload)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// RecordUIAction journals a ui.action event of a conversation as a finished
// turn of its own with no prompt, in one transaction. A running turn's
// journal has a single writer (its process), so another process never
// appends to it; like a system notice, this turn is never active and is
// written whatever turn the conversation has.
func (s *AIStore) RecordUIAction(profileID, conversationID string, p chatevents.UIActionPayload) (chatevents.Event, error) {
	if profileID == "" {
		return chatevents.Event{}, ErrProfileRequired
	}
	if _, err := s.GetConversation(conversationID, profileID); err != nil {
		return chatevents.Event{}, err
	}
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return chatevents.Event{}, fmt.Errorf("chat: ui action turn id: %w", err)
	}
	turnID := UIActionTurnPrefix + hex.EncodeToString(rnd[:])
	var ev chatevents.Event
	err := dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error {
		tx := dbconn.ConnTx{Ctx: context.Background(), Conn: conn}
		now := time.Now()
		stamp := now.UTC().Format(time.RFC3339)
		if _, err := tx.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, owner_instance_id, prompt, status, reason, last_committed_seq, created_at, updated_at)
			VALUES (?, ?, ?, '', '', ?, '', 0, ?, ?)`, turnID, conversationID, profileID, string(chatevents.StatusCompleted), stamp, stamp); err != nil {
			return fmt.Errorf("create ui action turn: %w", err)
		}
		var err error
		if ev, err = appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventUIAction, p, now); err != nil {
			return err
		}
		fin, err := appendEventTx(tx, profileID, conversationID, turnID, chatevents.EventTurnFinished,
			chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted}, now)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE ai_chat_turns SET last_committed_seq = ? WHERE id = ? AND profile_id = ?`, fin.Seq, turnID, profileID); err != nil {
			return fmt.Errorf("advance ui action turn: %w", err)
		}
		return nil
	})
	return ev, err
}
