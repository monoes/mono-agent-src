// Package commsync puts outbound approval items into Communications.
//
// Every outbound item that waits for a person's approval (a core.human_in_loop
// item in front of a publish/DM/email node, or a send an agent prepared in
// internal/hilsend) gets ONE person_messages row, direction 'outbound', when it
// is queued. That same row follows the item: its status moves
//
//	pending_approval -> approved -> sending -> sent
//
// or ends in rejected, failed or expired. The recipient (a person or a
// company) is saved in People first; an item without a recipient (a post) is
// linked to a People entry for our own account on that platform. The row's
// hil_ref is the hil_pending id and its external_id is "hil:"+id, so queuing
// the same item twice is idempotent.
//
// All status writes go through SetStatus, which refuses illegal moves.
package commsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is a queued message's lifecycle position (person_messages.status).
type Status string

const (
	PendingApproval Status = "pending_approval" // waiting for the operator
	Approved        Status = "approved"         // approved, not performed yet
	Sending         Status = "sending"          // being performed right now
	Sent            Status = "sent"             // performed
	Rejected        Status = "rejected"         // the operator said no; never performed
	Failed          Status = "failed"           // approved, but performing it failed
	Expired         Status = "expired"          // nobody decided before the timeout
)

// ErrIllegalTransition: SetStatus refused a move the lifecycle does not allow.
var ErrIllegalTransition = errors.New("illegal message status transition")

// allowed lists the moves the lifecycle allows. The end states have none, so
// a sent message never goes back to pending.
var allowed = map[Status][]Status{
	PendingApproval: {Approved, Rejected, Expired},
	Approved:        {Sending, Sent, Failed},
	Sending:         {Sent, Failed},
}

// CanTransition reports whether from -> to is a legal move. Staying put is
// not a move (SetStatus treats it as a no-op).
func CanTransition(from, to Status) bool {
	for _, t := range allowed[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Detail is what a status change may carry along.
type Detail struct {
	// Error is why the send failed; it lands in metadata.error.
	Error string
	// Edited holds the operator's edits of the item's editable fields (the
	// workflow approve path). Its text/subject fields replace the body and
	// subject of the row.
	Edited map[string]interface{}
	// Text and Subject replace the body/subject directly (a send item's edit).
	Text, Subject *string
}

// SetStatus moves the message queued for hilRef to status to. It is a no-op
// for an item without a Communications row and when the message already has
// that status, and returns ErrIllegalTransition (wrapped) for a move the
// lifecycle forbids. Every status write of this package goes through here.
func SetStatus(ctx context.Context, db *sql.DB, hilRef string, to Status, d Detail) error {
	if db == nil || hilRef == "" {
		return nil
	}
	var id, from, meta string
	err := db.QueryRowContext(ctx,
		`SELECT id, status, COALESCE(metadata,'') FROM person_messages WHERE hil_ref = ? ORDER BY created_at LIMIT 1`, hilRef).
		Scan(&id, &from, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("commsync: reading message of %s: %w", hilRef, err)
	}
	if Status(from) == to {
		return nil
	}
	if !CanTransition(Status(from), to) {
		return fmt.Errorf("%w: %s -> %s (item %s)", ErrIllegalTransition, from, to, hilRef)
	}

	m := map[string]interface{}{}
	_ = json.Unmarshal([]byte(meta), &m)
	text, subject := d.Text, d.Subject
	if len(d.Edited) > 0 {
		if k, _ := m["text_field"].(string); k != "" {
			if v, ok := d.Edited[k].(string); ok {
				text = &v
			}
		}
		if k, _ := m["subject_field"].(string); k != "" {
			if v, ok := d.Edited[k].(string); ok {
				subject = &v
			}
		}
	}
	now := time.Now().UTC()
	m["status_at"] = now.Format(time.RFC3339)
	if d.Error != "" {
		m["error"] = d.Error
	}
	raw, _ := json.Marshal(m)

	q := `UPDATE person_messages SET status = ?, metadata = ?`
	args := []interface{}{string(to), string(raw)}
	if text != nil {
		q += `, body = ?`
		args = append(args, *text)
	}
	if subject != nil {
		q += `, subject = ?`
		args = append(args, *subject)
	}
	if to == Sent {
		q += `, sent_at = ?`
		args = append(args, now)
	}
	q += ` WHERE id = ? AND status = ?`
	args = append(args, id, from)
	res, err := db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("commsync: setting %s to %s: %w", hilRef, to, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Someone else moved it between the read and the write: look again.
		return SetStatus(ctx, db, hilRef, to, d)
	}
	return nil
}

// SetText replaces the body and/or subject of a message still waiting for
// approval (the operator edited the item). Other states keep what was sent.
func SetText(ctx context.Context, db *sql.DB, hilRef string, text, subject *string) error {
	if db == nil || hilRef == "" || (text == nil && subject == nil) {
		return nil
	}
	q, args := `UPDATE person_messages SET `, []interface{}{}
	if text != nil {
		q += `body = ?,`
		args = append(args, *text)
	}
	if subject != nil {
		q += `subject = ?,`
		args = append(args, *subject)
	}
	q = strings.TrimSuffix(q, ",") + ` WHERE hil_ref = ? AND status = 'pending_approval'`
	args = append(args, hilRef)
	_, err := db.ExecContext(ctx, q, args...)
	return err
}

// SetStatusForExecution moves every message still pending_approval whose
// item belongs to the execution (a cancelled run's reviews).
func SetStatusForExecution(ctx context.Context, db *sql.DB, executionID string, to Status) error {
	return setMany(ctx, db, to, `
		SELECT pm.hil_ref FROM person_messages pm JOIN hil_pending h ON h.id = pm.hil_ref
		WHERE h.execution_id = ? AND pm.status = 'pending_approval'`, executionID)
}

// SetStatusForRejected moves every message still pending_approval whose
// hil_pending item has already been rejected to rejected (the engine's
// clean-up of items orphaned by a crash).
func SetStatusForRejected(ctx context.Context, db *sql.DB) error {
	return setMany(ctx, db, Rejected, `
		SELECT pm.hil_ref FROM person_messages pm JOIN hil_pending h ON h.id = pm.hil_ref
		WHERE h.status = 'rejected' AND pm.status = 'pending_approval'`)
}

func setMany(ctx context.Context, db *sql.DB, to Status, query string, args ...interface{}) error {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	var refs []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err == nil {
			refs = append(refs, r)
		}
	}
	rows.Close()
	var first error
	for _, r := range refs {
		if err := SetStatus(ctx, db, r, to, Detail{}); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// NodeStarted marks the approved messages whose publish node is about to run
// as sending. It is a no-op for a node nobody queued anything for.
//
// The returned error says which messages could not be marked (their row is left
// as it was, never half-moved); the caller reports it. It does not gate the
// send: whether a node may send is decided by the approval, not by this label.
func NodeStarted(ctx context.Context, db *sql.DB, executionID, nodeID string) error {
	refs, err := targeting(ctx, db, executionID, nodeID)
	for _, ref := range refs {
		if serr := setStatusRetry(ctx, db, ref, Sending, Detail{}); serr != nil {
			err = errors.Join(err, serr)
		}
	}
	return err
}

// NodeFinished records how the publish node ended: sent when err is nil,
// otherwise failed with the error. The returned error lists the messages it
// could not move (they stay approved/sending until someone sees the error).
func NodeFinished(ctx context.Context, db *sql.DB, executionID, nodeID string, err error) error {
	refs, rerr := targeting(ctx, db, executionID, nodeID)
	for _, ref := range refs {
		var serr error
		if err != nil {
			serr = setStatusRetry(ctx, db, ref, Failed, Detail{Error: err.Error()})
		} else if pending, perr := otherNodesPending(ctx, db, ref, nodeID); perr != nil {
			serr = perr
		} else if !pending {
			serr = setStatusRetry(ctx, db, ref, Sent, Detail{})
		}
		if serr != nil {
			rerr = errors.Join(rerr, serr)
		}
	}
	return rerr
}

// retryDelay is the pause before SetStatus is tried again after a write error
// (a busy database). A var so tests need not wait.
var retryDelay = 100 * time.Millisecond

// setStatusRetry is SetStatus with a bounded retry (three tries) for a write
// that failed for a reason other than an illegal move. It only repeats an
// idempotent label write: a move the lifecycle forbids is returned at once,
// and no attempt can turn a sent message back into a sendable one.
func setStatusRetry(ctx context.Context, db *sql.DB, hilRef string, to Status, d Detail) error {
	var err error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return err
			case <-time.After(retryDelay):
			}
		}
		if err = SetStatus(ctx, db, hilRef, to, d); err == nil || errors.Is(err, ErrIllegalTransition) {
			return err
		}
	}
	return err
}

// targeting returns the hil refs of approved/sending messages of the
// execution whose queued publish node is nodeID. A query error is returned,
// not turned into "nothing to do".
func targeting(ctx context.Context, db *sql.DB, executionID, nodeID string) ([]string, error) {
	if db == nil || executionID == "" || nodeID == "" {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT pm.hil_ref FROM person_messages pm JOIN hil_pending h ON h.id = pm.hil_ref
		WHERE h.execution_id = ? AND pm.status IN ('approved','sending')
		  AND EXISTS (SELECT 1 FROM json_each(pm.metadata, '$.workflow.target_nodes') WHERE value = ?)`,
		executionID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("commsync: finding the messages of node %s: %w", nodeID, err)
	}
	defer rows.Close()
	var refs []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return refs, fmt.Errorf("commsync: reading the messages of node %s: %w", nodeID, err)
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		return refs, fmt.Errorf("commsync: reading the messages of node %s: %w", nodeID, err)
	}
	return refs, nil
}

// LinkFor is the person a HIL item's Communications row belongs to: hilRef is
// the HIL id, so the HIL page can link to the person and the row. ok is false
// when the item queued no row (a HIL with nothing outbound after it).
func LinkFor(ctx context.Context, db *sql.DB, hilRef string) (personID, name string, ok bool) {
	err := db.QueryRowContext(ctx,
		`SELECT m.person_id, COALESCE(NULLIF(p.full_name,''), p.platform_username)
		 FROM person_messages m JOIN people p ON p.id = m.person_id
		 WHERE m.hil_ref = ? LIMIT 1`, hilRef).Scan(&personID, &name)
	return personID, name, err == nil
}
