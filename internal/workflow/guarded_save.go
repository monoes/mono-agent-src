package workflow

import (
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

// committedReviewHasher is a store that can say the ReviewHash of its committed rows.
type committedReviewHasher interface {
	committedReviewHash(ctx context.Context, id string) (string, error)
}

// ReviewHashOf is the hash to hand a guarded change for wf, a workflow as store served it: the hash
// of the committed rows when the store keeps rows (what the change compares, whichever way the file
// and the rows happen to represent an empty configuration), else ReviewHash(wf).
func ReviewHashOf(ctx context.Context, store WorkflowStore, wf *Workflow) (string, error) {
	if c, ok := store.(committedReviewHasher); ok {
		if hash, err := c.committedReviewHash(ctx, wf.ID); err != nil || hash != "" {
			return hash, err
		}
	}
	return ReviewHash(wf)
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

// SaveNodesGuarded is SaveWorkflowNodes as ONE critical section under the database's write lock
// (the lock every definition save takes, the operator's own included): it first takes the lock,
// compares the stored workflow's ReviewHash with guard.ExpectHash (ErrChangedSincePreview), writes
// the nodes, revokes approvals when asked, records the version, then publishes the canonical file
// atomically (temp file in the same folder, fsync, rename, fsync of the folder) and only then
// commits. A file step that fails rolls everything back (ErrFileSyncFailed); a commit that fails
// after the file was published puts the previous file bytes back. Every error is returned.
func (h *HybridWorkflowStore) SaveNodesGuarded(ctx context.Context, workflowID string, nodes []WorkflowNode, guard SaveGuard) error {
	_, err := h.Publish(ctx, Publication{
		WorkflowID:      workflowID,
		Origin:          "apply-fix",
		ExpectHash:      guard.ExpectHash,
		Guarded:         true,
		RevokeApprovals: guard.RevokeApprovals,
		KeepFileless:    true,
		Build: func(cur *Workflow) (*Workflow, error) {
			next := h.editBase(ctx, cur)
			next.Nodes = nodes
			return &next, nil
		},
	})
	return err
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
