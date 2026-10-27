package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// intent is a row of workflow_publications.
type intent struct {
	ID          int64
	WorkflowID  string
	Kind        string
	State       string
	BaseHash    string
	TargetHash  string
	TargetSHA   string
	PrevSHA     string
	PrevBytes   []byte
	PrevExisted bool
	OwnerPID    int
	StartedAt   time.Time
}

const intentColumns = `id, workflow_id, kind, state, base_hash, target_hash, target_sha, prev_sha, prev_bytes, prev_existed, owner_pid, started_at`

func scanIntent(row interface{ Scan(...any) error }) (*intent, error) {
	in := &intent{}
	var existed int
	var started sqliteTime
	err := row.Scan(&in.ID, &in.WorkflowID, &in.Kind, &in.State, &in.BaseHash, &in.TargetHash, &in.TargetSHA,
		&in.PrevSHA, &in.PrevBytes, &existed, &in.OwnerPID, &started)
	in.PrevExisted, in.StartedAt = existed != 0, started.Time
	return in, err
}

// live reports whether the process that opened the intent is still making the change.
func (in *intent) live() bool {
	return in.OwnerPID > 0 && time.Since(in.StartedAt) < publicationLease && processAlive(in.OwnerPID)
}

// quarantineDir is where a file nobody can attribute is copied, beside the workflows folder
// (every *.json inside the folder is read as a workflow).
func (s *WorkflowFileStore) quarantineDir() string {
	return filepath.Join(filepath.Dir(s.dir), "workflow-quarantine")
}

const dropIntentSQL = `DELETE FROM workflow_publications WHERE id = ?`

// resolveIntent makes the file agree with the committed rows again after a publication that did
// not finish, as a function of the intent and the file. It runs inside tx, which holds the write
// lock. It always rolls back: the commit removes its intent in the same transaction as the rows, so
// an intent that is still here proves the rows did not change (comparing hashes would not: an
// activation changes the rows but not the definition hash).
func (h *HybridWorkflowStore) resolveIntent(ctx context.Context, tx *sql.Tx, in *intent) (unresolved bool, err error) {
	raw, existed, err := h.files.readRaw(in.WorkflowID)
	if err != nil && ctx.Err() == nil {
		// Nobody can read it (too large, a pipe, a directory, no permission), so nobody can tell
		// which change it belongs to: leave it for the operator rather than failing every
		// later publication of this workflow (ResolveUnresolved keep-rows replaces it from the rows).
		why := "the workflow file cannot be read (" + err.Error() + ") and was left as it is"
		if errors.Is(err, ErrFileTooLarge) {
			why = "the workflow file is larger than " + fmt.Sprint(maxWorkflowFileBytes) + " bytes and was left as it is"
		}
		_, err = tx.ExecContext(ctx, `UPDATE workflow_publications SET state = 'unresolved', note = ? WHERE id = ?`, why, in.ID)
		return true, err
	}
	if err != nil {
		return false, err
	}
	file := ""
	if existed {
		file = shaHex(append([]byte{}, raw...))
	}
	drop := func() error {
		_, err := tx.ExecContext(ctx, dropIntentSQL, in.ID)
		return err
	}
	switch {
	case file == in.PrevSHA:
		return false, drop()
	case file == in.TargetSHA:
		if err := failpoint("during-restore"); err != nil {
			return false, err
		}
		if guardedRestoreFault != nil {
			if err := guardedRestoreFault(); err != nil {
				return false, err
			}
		}
		if err := h.files.restoreRaw(in.WorkflowID, in.PrevBytes, in.PrevExisted); err != nil {
			return false, err
		}
		return false, drop()
	}
	// Neither the file before nor the file the change wrote: an edit from outside landed in the
	// window. Keep a copy, leave the file alone, and let a person choose.
	dir := h.files.quarantineDir()
	if _, err := h.files.quarantine(in.WorkflowID, raw); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE workflow_publications SET state = 'unresolved', note = ? WHERE id = ?`,
		"the file changed during the change; a copy is in "+dir, in.ID)
	return true, err
}

// resolveByID resolves one intent under the write lock. Without force an intent whose owner is
// still working is left alone.
func (h *HybridWorkflowStore) resolveByID(ctx context.Context, id int64, force bool) error {
	if !force {
		// Look before locking: an intent that is being made, or is not pending, is none of our
		// business, and taking the write lock for it would make every reader wait on the writer.
		in, err := scanIntent(h.sql.db.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM workflow_publications WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) || (err == nil && (in.State != "pending" || in.live())) {
			return nil
		}
	}
	tx, done, err := h.beginDurable(ctx)
	if err != nil {
		return err
	}
	defer done()
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE workflow_publications SET state = state WHERE id = ?`, id); err != nil {
		return err
	}
	in, err := scanIntent(tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM workflow_publications WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if in.State != "pending" || (!force && in.live()) {
		return nil
	}
	unresolved, err := h.resolveIntent(ctx, tx, in)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if unresolved && force {
		return fmt.Errorf("the file changed while the change was in progress; a copy was kept")
	}
	return nil
}

func (h *HybridWorkflowStore) rollbackIntent(ctx context.Context, id int64) error {
	return h.resolveByID(ctx, id, true)
}

// pendingIntents lists the ids of unfinished publications, optionally of one workflow.
func (h *HybridWorkflowStore) pendingIntents(ctx context.Context, workflowID string) ([]int64, error) {
	q, args := `SELECT id FROM workflow_publications WHERE state = 'pending'`, []any(nil)
	if workflowID != "" {
		q, args = q+` AND workflow_id = ?`, []any{workflowID}
	}
	rows, err := h.sql.db.QueryContext(ctx, q, args...)
	if err != nil {
		if isNoTable(err) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RecoverPublications resolves every publication whose owner is gone and returns how many it
// looked at. Anyone may run it; a no-op query when nothing is open.
func (h *HybridWorkflowStore) RecoverPublications(ctx context.Context) (int, error) {
	if h.files == nil {
		return 0, nil
	}
	ids, err := h.pendingIntents(ctx, "")
	if err != nil {
		return 0, err
	}
	var errs []error
	for _, id := range ids {
		if err := h.resolveByID(ctx, id, false); err != nil {
			errs = append(errs, err)
		}
	}
	return len(ids), errors.Join(errs...)
}

func (h *HybridWorkflowStore) recoverID(ctx context.Context, workflowID string) error {
	ids, err := h.pendingIntents(ctx, workflowID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := h.resolveByID(ctx, id, false); err != nil {
			return err
		}
	}
	return nil
}

// openIntents is the set of workflow ids with a publication in flight or unresolved, after
// resolving the abandoned ones: for these the committed rows, not the file, are the definition. A
// failed read of the coordination tables is an error, never an empty set: the file may only be
// trusted when it is known that no change is open.
func (h *HybridWorkflowStore) openIntents(ctx context.Context) (map[string]bool, error) {
	if h.files == nil {
		return nil, nil
	}
	read := func() (map[string]bool, error) {
		rows, err := h.sql.db.QueryContext(ctx, `SELECT workflow_id FROM workflow_publications`)
		if err != nil {
			if isNoTable(err) {
				return nil, nil // a database that predates the table has no changes in flight
			}
			return nil, err
		}
		defer rows.Close()
		var out map[string]bool
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return nil, err
			}
			if out == nil {
				out = map[string]bool{}
			}
			out[id] = true
		}
		return out, rows.Err()
	}
	open, err := read()
	if err != nil || len(open) == 0 {
		return nil, err
	}
	n, rerr := h.RecoverPublications(ctx)
	if n > 0 {
		if open, err = read(); err != nil {
			return nil, err
		}
	}
	if rerr != nil && ctx.Err() != nil {
		return nil, rerr
	}
	return open, nil
}

// publicationGen counts the intents ever recorded; it moves whenever a change starts.
func (h *HybridWorkflowStore) publicationGen(ctx context.Context) (int64, error) {
	var n int64
	err := h.sql.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM sqlite_sequence WHERE name = 'workflow_publications'`).Scan(&n)
	if err != nil && isNoTable(err) {
		return 0, nil
	}
	return n, err
}

// gated reports whether the file of workflow id must not be trusted (see stableFileRead).
func gated(open map[string]bool, id string) bool { return open[id] || open["*"] }

// stableFileRead runs read, which loads workflow files, with the set of workflows that have a
// change in flight (their files are skipped). A change that started while read ran may have
// replaced a file before read opened it, or may be rolled back before this call could see it, so
// the read is repeated until no change started during it; if the files will not hold still, every
// file is skipped and the committed rows speak for all of them.
func (h *HybridWorkflowStore) stableFileRead(ctx context.Context, read func(open map[string]bool) error) error {
	for i := 0; i < 5; i++ {
		gen, err := h.publicationGen(ctx)
		if err != nil {
			return err
		}
		open, err := h.openIntents(ctx)
		if err != nil {
			return err
		}
		if err := read(open); err != nil {
			return err
		}
		after, err := h.publicationGen(ctx)
		if err != nil {
			return err
		}
		if after == gen {
			return nil
		}
	}
	return read(map[string]bool{"*": true})
}
