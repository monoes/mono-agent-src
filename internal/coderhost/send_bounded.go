package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// ErrTooManyWaiting is what EnqueueSendBounded answers when the conversation already has as many commands waiting as the caller
// allows.
var ErrTooManyWaiting = errors.New("coder host: too many commands already wait for this conversation")

// EnqueueSendBounded queues a send under two bounds that are checked and applied in ONE write transaction, so callers that
// run at once cannot pass them together: at most maxPending commands wait for the conversation (more: ErrTooManyWaiting), and
// when within is above 0 a send with exactly this message queued within that time that still waits is the answer
// (deduped true, its id) instead of a new row.
func (s *Store) EnqueueSendBounded(c Command, maxPending int, within time.Duration) (out Command, deduped bool, err error) {
	err = dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error {
		q := connQuerier{conn}
		if within > 0 {
			var id int64
			if q.QueryRow(`SELECT id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND status = 'pending'
				AND message = ? AND `+tsInWindow("created_at", s.now().Add(-within))+` ORDER BY id DESC LIMIT 1`, c.ProfileID, c.ConversationID, c.Message).Scan(&id) == nil {
				out, deduped = Command{ID: id, ProfileID: c.ProfileID, ConversationID: c.ConversationID}, true
				return nil
			}
		}
		var waiting int
		if err := q.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND status = 'pending'`, c.ProfileID, c.ConversationID).Scan(&waiting); err != nil {
			return err
		}
		if waiting >= maxPending {
			return ErrTooManyWaiting
		}
		got, err := s.enqueueOn(q, c)
		out = got
		return err
	})
	return out, deduped, err
}
