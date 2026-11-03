package queue

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

const selectCols = `qid, profile, generation, lead, class, bytes, payload, payload_sha, state, enq_at, mac`

type row struct {
	QID        int64
	Profile    string
	Generation int64
	Lead       string
	Class      governor.Class
	Bytes      int64
	Payload    string
	SHA        [32]byte
	State      governor.QueueState
	EnqAt      int64
	MAC        []byte
}

type scanner interface{ Scan(dest ...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	var class, state string
	var sha []byte
	if err := s.Scan(&r.QID, &r.Profile, &r.Generation, &r.Lead, &class, &r.Bytes, &r.Payload, &sha, &state, &r.EnqAt, &r.MAC); err != nil {
		return row{}, err
	}
	r.Class, r.State = governor.Class(class), governor.QueueState(state)
	copy(r.SHA[:], sha)
	return r, nil
}

func i64(v int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return b[:]
}

// mac covers profile, generation, qid, lead, class, bytes, payload_sha, state and enq_at (governor.MACDomainQueue).
func (q *Queue) mac(r row) ([]byte, error) {
	return q.keys.MAC(governor.MACDomainQueue, []byte(r.Profile), i64(r.Generation), i64(r.QID), []byte(r.Lead),
		[]byte(r.Class), i64(r.Bytes), r.SHA[:], []byte(r.State), i64(r.EnqAt))
}

// verify says whether the row is what the queue wrote: the MAC verifies, and while the payload is still held it is the
// bytes the hash and the byte count name.
func (q *Queue) verify(r row) bool {
	want, err := q.mac(r)
	if err != nil || !hmac.Equal(want, r.MAC) {
		return false
	}
	if r.State == governor.QueueCancelled {
		return r.Payload == "" || q.payloadMatches(r)
	}
	return q.payloadMatches(r)
}

func (q *Queue) payloadMatches(r row) bool {
	return int64(len(r.Payload)) == r.Bytes && sha256.Sum256([]byte(r.Payload)) == r.SHA
}

// moveTo writes the forward step queued -> to for r. The new MAC is made only from a row that verified; a row that did
// not keeps its old (non-verifying) MAC, so it can never look sound afterwards. A cancelled row drops its payload.
func (q *Queue) moveTo(ctx context.Context, x governor.DB, r row, to governor.QueueState, sound bool) error {
	n := r
	n.State = to
	if to == governor.QueueCancelled {
		n.Payload = ""
	}
	mac := r.MAC
	if sound {
		var err error
		if mac, err = q.mac(n); err != nil {
			return err
		}
	}
	res, err := x.ExecContext(ctx, `UPDATE doomsday_queue SET state = ?, payload = ?, mac = ? WHERE qid = ? AND profile = ? AND generation = ? AND state = 'queued'`,
		string(to), n.Payload, mac, r.QID, r.Profile, r.Generation)
	if err != nil {
		return mapErr(err)
	}
	if c, _ := res.RowsAffected(); c != 1 {
		return fmt.Errorf("%w: qid %d", governor.ErrNotQueued, r.QID)
	}
	return nil
}
