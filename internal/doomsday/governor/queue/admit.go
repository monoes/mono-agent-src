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

// CancelAll moves every queued item to cancelled and returns how many. It does not need a running run: a stopped run's
// queue is cancelled by Stop and never resumes. A row that does not verify is cancelled too and keeps its
// non-verifying MAC.
func (q *Queue) CancelAll(ctx context.Context, profile string, generation int64) (int, error) {
	n := 0
	err := q.tx(ctx, func(x governor.DB) error {
		rows, err := x.QueryContext(ctx, `SELECT `+selectCols+` FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued' ORDER BY qid`, profile, generation)
		if err != nil {
			return err
		}
		var pending []row
		for rows.Next() {
			r, err := scanRow(rows)
			if err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, r := range pending {
			if err := q.moveTo(ctx, x, r, governor.QueueCancelled, q.verify(r)); err != nil {
				return err
			}
		}
		n = len(pending)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Cancel is not implemented yet.
func (q *Queue) Cancel(ctx context.Context, profile string, generation int64, qid int64) error {
	return fmt.Errorf("todo")
}
