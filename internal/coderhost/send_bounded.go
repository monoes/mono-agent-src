package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
	"github.com/monoes/mono-agent/internal/tasks"
)

// ErrTooManyWaiting is what EnqueueSendBounded answers when the conversation already has as many person messages waiting as the
// caller allows.
var ErrTooManyWaiting = errors.New("coder host: too many person messages already wait for this conversation")

// ErrTurnExists is what EnqueueSendBounded answers, when asked for a conversation with no turn yet, if it has one.
var ErrTurnExists = errors.New("coder host: the conversation already had a turn")

// SendBounds are the bounds EnqueueSendBounded holds.
type SendBounds struct {
	// MaxPending is how many person messages (sends with a t- turn id and no issuer) may wait for the conversation; the ask,
	// check-in and master channels are not counted, so a descendant lead cannot fill the slots.
	MaxPending int
	// Within, above 0, makes a person message with exactly this text that still waits and was queued within it the answer.
	Within time.Duration
	// NoTurnYet makes the call refuse (ErrTurnExists) when the conversation already has a turn.
	NoTurnYet bool
}

// IsBossConversation says whether the conversation is one `boss new` made (stamped or not): the host composes nothing from other
// sessions into a boss's turn, and the guards on a turn into it ask the same question.
func (s *Store) IsBossConversation(profileID, conversationID string) bool {
	ok, err := tasks.IsBossAnyConversation(context.Background(), s.db, profileID, conversationID)
	return err == nil && ok
}

// EnqueueSendBounded queues a PERSON'S send (kind send, a t- turn id, no issuer: nothing the ask, check-in or master channels
// make) under the bounds of b, all checked and applied in ONE write transaction, so callers that run at once cannot pass them
// together. Only the user's own CLI verbs call it (an AST test holds that list).
func (s *Store) EnqueueSendBounded(c Command, b SendBounds) (out Command, deduped bool, err error) {
	if c.Kind != KindSend || c.Issuer != "" || !strings.HasPrefix(c.TurnID, "t-") {
		return Command{}, false, fmt.Errorf("%w: a bounded send is a person's send (kind send, a t- turn id, no issuer)", ErrInvalid)
	}
	err = dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error {
		q := connQuerier{conn}
		if b.NoTurnYet {
			var turns int
			if err := q.QueryRow(`SELECT COUNT(*) FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ?`, c.ProfileID, c.ConversationID).Scan(&turns); err != nil {
				return err
			}
			if turns > 0 {
				return ErrTurnExists
			}
		}
		if b.Within > 0 {
			var id int64
			if q.QueryRow(`SELECT id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND status = 'pending'
				AND message = ? AND `+tsInWindow("created_at", s.now().Add(-b.Within))+` ORDER BY id DESC LIMIT 1`, c.ProfileID, c.ConversationID, c.Message).Scan(&id) == nil {
				out, deduped = Command{ID: id, ProfileID: c.ProfileID, ConversationID: c.ConversationID}, true
				return nil
			}
		}
		var waiting int
		if err := q.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND status = 'pending'
			AND kind = 'send' AND issuer = '' AND substr(turn_id, 1, 2) = 't-'`, c.ProfileID, c.ConversationID).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= b.MaxPending {
			return ErrTooManyWaiting
		}
		got, err := s.enqueueOn(q, c)
		out = got
		return err
	})
	return out, deduped, err
}
