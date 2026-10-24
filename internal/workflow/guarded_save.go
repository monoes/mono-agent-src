package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrChangedSincePreview: the workflow is not the one the caller reviewed.
var ErrChangedSincePreview = errors.New("the workflow changed since it was previewed")

// ReviewHash identifies what a person looked at before agreeing to an edit: the
// definition the approval code hashes (name, description, settings, nodes,
// connections) together with whether the workflow is active. Two reads of the
// same stored workflow hash the same, from either store.
func ReviewHash(w *Workflow) (string, error) {
	def, err := definitionHash(w)
	if err != nil {
		return "", err
	}
	state := "inactive"
	if w.IsActive {
		state = "active"
	}
	sum := sha256.Sum256([]byte(def + "|" + state))
	return hex.EncodeToString(sum[:]), nil
}

// SaveGuard conditions SaveNodesGuarded.
type SaveGuard struct {
	// ExpectHash is the ReviewHash the stored workflow must still have.
	ExpectHash string
	// RevokeApprovals removes every stored approval of the workflow in the same
	// transaction, so the result needs a fresh one even when its definition
	// equals an older approved one.
	RevokeApprovals bool
}

// ErrFileSyncFailed: the canonical workflow file could not be published, so nothing was changed.
var ErrFileSyncFailed = errors.New("the workflow file could not be written")

// ErrFileInconsistent: the workflow file could not be put back after a failed save, so the file
// and the database may disagree about this workflow.
var ErrFileInconsistent = errors.New("the workflow file and the database may disagree")

// fileWriteMu orders the writes of workflow files in this process: a guarded save holds it from
// publishing its file to its commit, and the unguarded paths (the file sync after a node edit, a
// plain file save) take it around theirs.
var fileWriteMu sync.Mutex

// Test seams, nil in production: a failure injected where the canonical file is published,
// where the transaction commits, and where a previous file is put back.
var guardedFileFault, guardedCommitFault, guardedRestoreFault func() error

type txHookKey struct{}

// WithTxHook makes the next definition save made with ctx call fn inside its database
// transaction, after the rows are written and before the file is published and the transaction
// commits. An error from fn aborts the save as a *TxHookError. The apply-once claim of an AI
// task proposal uses it, so the claim commits or rolls back with the write it guards.
func WithTxHook(ctx context.Context, fn func(ctx context.Context, tx *sql.Tx) error) context.Context {
	return context.WithValue(ctx, txHookKey{}, fn)
}

func txHookFrom(ctx context.Context) func(context.Context, *sql.Tx) error {
	fn, _ := ctx.Value(txHookKey{}).(func(context.Context, *sql.Tx) error)
	return fn
}

// TxHookError wraps what a WithTxHook function returned.
type TxHookError struct{ Err error }

func (e *TxHookError) Error() string { return e.Err.Error() }
func (e *TxHookError) Unwrap() error { return e.Err }

// restoreFile puts previous file bytes back, durably, naming the workflow when it cannot.
func (h *HybridWorkflowStore) restorePrevious(id string, prev []byte) error {
	var err error
	if guardedRestoreFault != nil {
		err = guardedRestoreFault()
	}
	if err == nil {
		err = h.files.restoreRaw(id, prev, true)
	}
	if err != nil {
		return fmt.Errorf("%w: workflow %s: %v", ErrFileInconsistent, id, err)
	}
	return nil
}

// SaveNodesGuarded is SaveWorkflowNodes as ONE critical section under the database's write lock
// (the lock every definition save takes, the operator's own included): it first takes the lock,
// compares the stored workflow's ReviewHash with guard.ExpectHash (ErrChangedSincePreview), writes
// the nodes, revokes approvals when asked, records the version, then publishes the canonical file
// atomically (temp file in the same folder, fsync, rename, fsync of the folder) and only then
// commits. A file step that fails rolls everything back (ErrFileSyncFailed); a commit that fails
// after the file was published puts the previous file bytes back. Every error is returned.
func (h *HybridWorkflowStore) SaveNodesGuarded(ctx context.Context, workflowID string, nodes []WorkflowNode, guard SaveGuard) error {
	h.ensureSQLWorkflow(ctx, workflowID)
	tx, err := h.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning guarded save: %w", err)
	}
	defer tx.Rollback()
	// A write first: this connection now holds the write lock, so nothing can
	// change the workflow between the comparison below and the commit.
	res, err := tx.ExecContext(ctx, `UPDATE workflows SET updated_at = updated_at WHERE id = ?`, workflowID)
	if err != nil {
		return fmt.Errorf("locking workflow %s: %w", workflowID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("workflow %s not found", workflowID)
	}
	cur, err := getWorkflowFrom(ctx, tx, workflowID)
	if err != nil {
		return err
	}
	have, err := ReviewHash(cur)
	if err != nil {
		return err
	}
	if have != guard.ExpectHash {
		return ErrChangedSincePreview
	}
	if err := saveNodesTx(ctx, tx, workflowID, nodes); err != nil {
		return err
	}
	if guard.RevokeApprovals {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_approvals WHERE workflow_id = ?`, workflowID); err != nil && !isNoTable(err) {
			return fmt.Errorf("revoking approvals: %w", err)
		}
	}
	var rowVersion int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM workflows WHERE id = ?`, workflowID).Scan(&rowVersion); err != nil {
		return fmt.Errorf("reading version of workflow %s: %w", workflowID, err)
	}
	next := *cur
	next.Nodes = nodes
	version, _, err := recordVersionTx(ctx, tx, &next, rowVersion, false, "")
	if err != nil {
		return err
	}
	if hook := txHookFrom(ctx); hook != nil {
		if err := hook(ctx, tx); err != nil {
			return &TxHookError{Err: err}
		}
	}
	fileWriteMu.Lock()
	restore, err := h.publishNodesFile(ctx, workflowID, nodes, cur.Connections, version)
	if err != nil {
		fileWriteMu.Unlock()
		return err
	}
	if guardedCommitFault != nil {
		err = guardedCommitFault()
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		err = fmt.Errorf("committing guarded save: %w", err)
		if restore != nil {
			if rerr := restore(); rerr != nil {
				err = errors.Join(err, rerr)
			}
		}
		fileWriteMu.Unlock()
		return err
	}
	fileWriteMu.Unlock()
	return h.saved(ctx, workflowID, nil)
}

// publishNodesFile writes the canonical file with nodes and conns, atomically and durably, and
// returns the function that puts the previous bytes back. A workflow without a file (SQLite only)
// has nothing to publish.
func (h *HybridWorkflowStore) publishNodesFile(ctx context.Context, id string, nodes []WorkflowNode, conns []WorkflowConnection, version int) (func() error, error) {
	if h.files == nil {
		return nil, nil
	}
	fail := func(err error) (func() error, error) { return nil, fmt.Errorf("%w: %v", ErrFileSyncFailed, err) }
	prev, existed, err := h.files.readRaw(id)
	if err != nil {
		return fail(err)
	}
	if !existed {
		return nil, nil
	}
	fw, err := h.files.GetWorkflow(ctx, id)
	if err != nil {
		return fail(err)
	}
	if fw == nil {
		return nil, nil
	}
	fw.Nodes, fw.Connections, fw.Version = nodes, conns, version
	restore := func() error { return h.restorePrevious(id, prev) }
	err = nil
	if guardedFileFault != nil {
		err = guardedFileFault()
	}
	if err == nil {
		err = h.files.saveWorkflowDurable(ctx, fw)
	}
	if err != nil {
		// The rename may have happened before the failure (the folder flush that follows it can
		// fail): put the old bytes back unless the file is untouched.
		if now, there, rerr := h.files.readRaw(id); rerr == nil && there && bytes.Equal(now, prev) {
			return fail(err)
		}
		if rerr := restore(); rerr != nil {
			return nil, errors.Join(fmt.Errorf("%w: %v", ErrFileSyncFailed, err), rerr)
		}
		return fail(err)
	}
	return restore, nil
}

// RevokeApprovals removes every stored approval of workflow id.
func (h *HybridWorkflowStore) RevokeApprovals(ctx context.Context, workflowID string) error {
	_, err := h.sql.db.ExecContext(ctx, `DELETE FROM workflow_approvals WHERE workflow_id = ?`, workflowID)
	if err != nil && !isNoTable(err) {
		return err
	}
	return nil
}

func isNoTable(err error) bool { return err != nil && strings.Contains(err.Error(), "no such table") }

// getWorkflowFrom is SQLiteWorkflowStore.GetWorkflow over a transaction.
func getWorkflowFrom(ctx context.Context, q rowQuerier, id string) (*Workflow, error) {
	w := &Workflow{}
	var isActive int
	var createdAt, updatedAt sqliteTime
	err := q.QueryRowContext(ctx, `
		SELECT id, name, description, is_active, version, profile_id, created_at, updated_at
		FROM workflows WHERE id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.Description, &isActive, &w.Version, &w.ProfileID, &createdAt, &updatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting workflow %s: %w", id, err)
	}
	w.CreatedAt, w.UpdatedAt = createdAt.Time, updatedAt.Time
	w.IsActive = isActive != 0
	loadSettingsFrom(ctx, q, w)
	if w.Nodes, err = loadNodesFrom(ctx, q, id); err != nil {
		return nil, err
	}
	if w.Connections, err = loadConnectionsFrom(ctx, q, id); err != nil {
		return nil, err
	}
	return w, nil
}
