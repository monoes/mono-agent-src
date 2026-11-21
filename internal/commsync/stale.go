package commsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// StaleSendingAfter is how long a message may be 'sending' before it is worth a look. A send can
// legitimately take a while, so this is deliberately conservative.
const StaleSendingAfter = time.Hour

// StaleSendingGivesUpAfter is when a 'sending' row whose execution is over is ended as failed. No send
// runs a day, so past it the label can only be a leftover of a crash; the real outcome is unknown.
const StaleSendingGivesUpAfter = 24 * time.Hour

// staleSendingNote is why a swept message is failed. It says the send may have gone out, so nobody
// resends on the strength of "failed" alone.
const staleSendingNote = "outcome unknown: the app stopped before it recorded whether this was sent; check the platform before sending it again"

// StaleSendingRow is a message that has been 'sending' longer than StaleSendingAfter.
type StaleSendingRow struct {
	ID     string
	HilRef string
	Age    time.Duration
}

// ListStaleSending lists the messages that have been 'sending' for over StaleSendingAfter, oldest first.
// It only reads: a stale row might still be going out (or might have gone out), so nothing here fails
// or retries it; that is a decision for the operator.
func ListStaleSending(ctx context.Context, db *sql.DB, now time.Time) ([]StaleSendingRow, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, COALESCE(hil_ref,''), COALESCE(metadata,''), CAST(created_at AS TEXT) FROM person_messages WHERE status = ?`, string(Sending))
	if err != nil {
		return nil, fmt.Errorf("commsync: listing sending messages: %w", err)
	}
	defer rows.Close()
	var out []StaleSendingRow
	for rows.Next() {
		var id, ref, meta, created string
		if err := rows.Scan(&id, &ref, &meta, &created); err != nil {
			return nil, err
		}
		since, ok := sendingSince(meta, created)
		if !ok {
			continue
		}
		if age := now.Sub(since); age >= StaleSendingAfter {
			out = append(out, StaleSendingRow{ID: id, HilRef: ref, Age: age})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// oldest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Age > out[j-1].Age; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// sendingSince is when the row entered its current status: metadata.status_at, else created_at.
func sendingSince(meta, created string) (time.Time, bool) {
	m := map[string]interface{}{}
	_ = json.Unmarshal([]byte(meta), &m)
	if s, _ := m["status_at"].(string); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, created); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// SweepStaleSending ends the messages that have been 'sending' for over StaleSendingGivesUpAfter and
// whose execution is no longer RUNNING, QUEUED or WAITING (or no longer exists) as failed, with a note
// that the outcome is unknown. It returns how many it ended.
//
// Failed is terminal in the lifecycle, so this can never make a message sendable again: nothing is
// retried, and a message that did go out is not sent twice. A row of a live execution is left alone
// at any age. Errors on single rows are joined and the others still move.
func SweepStaleSending(ctx context.Context, db *sql.DB, now time.Time) (int, error) {
	if db == nil {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT pm.hil_ref, COALESCE(pm.metadata,''), CAST(pm.created_at AS TEXT)
		FROM person_messages pm
		LEFT JOIN hil_pending h ON h.id = pm.hil_ref
		LEFT JOIN workflow_executions e ON e.id = h.execution_id
		WHERE pm.status = ? AND pm.hil_ref IS NOT NULL AND pm.hil_ref != ''
		  AND (e.id IS NULL OR e.status NOT IN ('RUNNING','QUEUED','WAITING'))`, string(Sending))
	if err != nil {
		return 0, fmt.Errorf("commsync: finding abandoned sending messages: %w", err)
	}
	var refs []string
	for rows.Next() {
		var ref, meta, created string
		if err := rows.Scan(&ref, &meta, &created); err != nil {
			rows.Close()
			return 0, err
		}
		if since, ok := sendingSince(meta, created); ok && now.Sub(since) >= StaleSendingGivesUpAfter {
			refs = append(refs, ref)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var first error
	n := 0
	for _, ref := range refs {
		if err := setStatusRetry(ctx, db, ref, Failed, Detail{Error: staleSendingNote}); err != nil {
			first = errors.Join(first, err)
			continue
		}
		n++
	}
	return n, first
}
