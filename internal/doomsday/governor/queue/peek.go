package queue

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// peekSQL orders the pending rows of a run: boss class first; inside a class by round (a lead's n-th pending item is
// in round n, so one lead's backlog never delays another lead's first item by more than one round); inside a round the
// lead served least so far (admitted rows) first, then the lead whose oldest pending item is oldest; FIFO within a
// lead. qid is unique, so the order is total and deterministic.
const peekSQL = `
WITH pend AS (
  SELECT ` + selectCols + `,
         row_number() OVER (PARTITION BY class, lead ORDER BY qid) AS rn,
         min(qid)     OVER (PARTITION BY class, lead)              AS head
  FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued'),
served AS (SELECT lead, count(*) AS n FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'admitted' GROUP BY lead)
SELECT ` + selectCols + ` FROM pend LEFT JOIN served USING (lead)
ORDER BY (class = 'boss') DESC, rn, COALESCE(served.n, 0), head, qid
LIMIT ?`

// Peek lists pending items in service order. It changes nothing. max is clamped to 1..MaxPeek (a smaller or negative
// value yields nothing). A row that does not verify is left out: the scheduler never sees it, and MarkAdmitted or
// CancelAll disposes of it.
func (q *Queue) Peek(ctx context.Context, profile string, generation int64, max int) ([]governor.Item, error) {
	if max <= 0 {
		return nil, nil
	}
	if max > MaxPeek {
		max = MaxPeek
	}
	rows, err := q.db.QueryContext(ctx, peekSQL, profile, generation, profile, generation, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []governor.Item
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		if !q.verify(r) {
			continue
		}
		out = append(out, governor.Item{QID: r.QID, Lead: r.Lead, Class: r.Class, Payload: []byte(r.Payload), EnqAt: r.EnqAt})
	}
	return out, rows.Err()
}
