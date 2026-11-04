package queue

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// MarkAdmitted moves one item queued -> admitted in one transaction, after the row's MAC and payload hash verified. A
// row that does not verify is never admitted: it is cancelled (committed) and ErrRowTampered is returned. A crash
// before the commit leaves the row queued. Admission needs a running run (ErrNotRunning otherwise); the payload is kept
// on an admitted row so a launch lost after the commit can be recovered by hand.
func (q *Queue) MarkAdmitted(ctx context.Context, profile string, generation int64, qid int64) error {
	var tampered bool
	err := q.tx(ctx, func(x governor.DB) error {
		r, err := scanRow(x.QueryRowContext(ctx, `SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ? AND profile = ? AND generation = ?`, qid, profile, generation))
		if err != nil {
			return fmt.Errorf("%w: qid %d", governor.ErrNotQueued, qid)
		}
		if r.State != governor.QueueQueued {
			return fmt.Errorf("%w: qid %d is %s", governor.ErrNotQueued, qid, r.State)
		}
		if !q.verify(r) {
			tampered = true
			return q.moveTo(ctx, x, r, governor.QueueCancelled, false)
		}
		return q.moveTo(ctx, x, r, governor.QueueAdmitted, true)
	})
	if err != nil {
		return err
	}
	if tampered {
		return fmt.Errorf("%w: qid %d", ErrRowTampered, qid)
	}
	return nil
}

// cancelBatch bounds the rows one cancel transaction touches, so Stop's own writes never wait on a long hold.
const cancelBatch = 50

// cancelPasses bounds the rounds CancelAll re-reads the queue to catch a row written while it worked.
const cancelPasses = 4

// prepared is a queued row with the cancelled row's MAC already computed: the key work happens outside any transaction.
type prepared struct {
	r   row
	mac []byte
}

// CancelAll moves every queued item to cancelled and returns how many. It does not need a running run: a stopped run's
// queue is cancelled by Stop and never resumes. A row that does not verify is cancelled too and keeps its non-verifying
// MAC. Rows are read and verified (and the new MACs made) OUTSIDE the write transaction, which only applies the updates in
// small batches: a slow key store never holds the database's write lock, so Stop's own writes do not meet SQLITE_BUSY.
func (q *Queue) CancelAll(ctx context.Context, profile string, generation int64) (int, error) {
	total := 0
	for pass := 0; pass < cancelPasses; pass++ {
		prep, err := q.prepareCancel(ctx, profile, generation)
		if err != nil {
			return total, err
		}
		if len(prep) == 0 {
			break
		}
		for i := 0; i < len(prep); i += cancelBatch {
			n, err := q.applyCancel(ctx, prep[i:min(i+cancelBatch, len(prep))])
			total += n
			if err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (q *Queue) prepareCancel(ctx context.Context, profile string, generation int64) ([]prepared, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT `+selectCols+` FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued' ORDER BY qid`, profile, generation)
	if err != nil {
		return nil, err
	}
	var pending []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]prepared, 0, len(pending))
	for _, r := range pending {
		p, err := q.prepareOne(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// prepareOne makes the cancelled row's MAC from a row that verified; a row that did not keeps its old MAC.
func (q *Queue) prepareOne(r row) (prepared, error) {
	p := prepared{r: r, mac: r.MAC}
	if q.verify(r) {
		n := r
		n.State, n.Payload = governor.QueueCancelled, ""
		mac, err := q.mac(n)
		if err != nil {
			return prepared{}, err
		}
		p.mac = mac
	}
	return p, nil
}

// applyCancel writes one batch in one transaction. A row that is no longer queued (admitted meanwhile) is left alone.
func (q *Queue) applyCancel(ctx context.Context, batch []prepared) (int, error) {
	n := 0
	err := q.tx(ctx, func(x governor.DB) error {
		n = 0
		for _, p := range batch {
			res, err := x.ExecContext(ctx, `UPDATE doomsday_queue SET state = 'cancelled', payload = '', mac = ? WHERE qid = ? AND profile = ? AND generation = ? AND state = 'queued'`,
				p.mac, p.r.QID, p.r.Profile, p.r.Generation)
			if err != nil {
				return mapErr(err)
			}
			if c, _ := res.RowsAffected(); c == 1 {
				n++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Cancel moves one queued item to cancelled: a launch whose send is gone. ErrNotQueued when the row is not queued.
func (q *Queue) Cancel(ctx context.Context, profile string, generation int64, qid int64) error {
	r, err := scanRow(q.db.QueryRowContext(ctx, `SELECT `+selectCols+` FROM doomsday_queue WHERE qid = ? AND profile = ? AND generation = ?`, qid, profile, generation))
	if err != nil || r.State != governor.QueueQueued {
		return fmt.Errorf("%w: qid %d", governor.ErrNotQueued, qid)
	}
	p, err := q.prepareOne(r)
	if err != nil {
		return err
	}
	n, err := q.applyCancel(ctx, []prepared{p})
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("%w: qid %d", governor.ErrNotQueued, qid)
	}
	return nil
}
