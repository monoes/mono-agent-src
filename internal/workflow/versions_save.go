package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// SaveWorkflow writes w's whole definition — the workflow row, its nodes and
// connections, and a version record when the definition changed — in one
// transaction: either all of it lands or none of it does. w.Version is set
// to the definition's version.
func (s *SQLiteWorkflowStore) SaveWorkflow(ctx context.Context, w *Workflow) error {
	if w.ID == "" {
		w.ID = newID()
	}
	return s.saveDefinition(ctx, w, true, 0, "", nil)
}

// writeDefinitionRows writes w's workflow row, settings, nodes and connections and records its
// version inside tx, and returns that version. The caller commits.
func writeDefinitionRows(ctx context.Context, tx *sql.Tx, w *Workflow, mirrorGraph bool, storedVersion int, note string) (int, error) {
	now := time.Now().UTC()
	created := w.CreatedAt
	if created.IsZero() {
		created = now
	}
	profileID := w.ProfileID
	if profileID == "" {
		return 0, fmt.Errorf("saving workflow %s: %w", w.ID, ErrNoProfile)
	}
	// The profile and version of an existing row are not the saver's to
	// change: the profile is set where the workflow is created, the version
	// by recordVersionTx below.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO workflows (id, name, description, is_active, version, profile_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, description = excluded.description,
			is_active = excluded.is_active, updated_at = excluded.updated_at`,
		w.ID, w.Name, w.Description, boolToInt(w.IsActive), profileID, created, now); err != nil {
		return 0, fmt.Errorf("saving workflow %s: %w", w.ID, err)
	}
	saveWorkflowSettings(ctx, tx, w)
	var rowVersion int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM workflows WHERE id = ?`, w.ID).Scan(&rowVersion); err != nil {
		return 0, fmt.Errorf("reading version of workflow %s: %w", w.ID, err)
	}
	if mirrorGraph {
		if err := saveNodesTx(ctx, tx, w.ID, append([]WorkflowNode(nil), w.Nodes...)); err != nil {
			return 0, fmt.Errorf("saving workflow %s nodes: %w", w.ID, err)
		}
		if err := saveConnectionsTx(ctx, tx, w.ID, append([]WorkflowConnection(nil), w.Connections...)); err != nil {
			return 0, fmt.Errorf("saving workflow %s connections: %w", w.ID, err)
		}
	}
	v, _, err := recordVersionTx(ctx, tx, w, max(storedVersion, rowVersion), false, note)
	return v, err
}

// saveDefinition is the transaction behind SaveWorkflow. mirrorGraph false
// leaves nodes and connections alone (see ErrSharedIDs). storedVersion is the
// version the caller saw before the save. beforeCommit runs last inside the
// transaction — the hybrid store writes the workflow file there — and its
// error rolls everything back. On any error w.Version is left as it was.
func (s *SQLiteWorkflowStore) saveDefinition(ctx context.Context, w *Workflow, mirrorGraph bool, storedVersion int, note string, beforeCommit func() error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning save-workflow transaction: %w", err)
	}
	defer tx.Rollback()

	v, err := writeDefinitionRows(ctx, tx, w, mirrorGraph, storedVersion, note)
	if err != nil {
		return err
	}

	if hook := txHookFrom(ctx); hook != nil {
		if err := hook(ctx, tx); err != nil {
			return &TxHookError{Err: err}
		}
	}
	prevVersion := w.Version
	w.Version = v
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			w.Version = prevVersion
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		w.Version = prevVersion
		return fmt.Errorf("committing save-workflow transaction: %w", err)
	}
	return nil
}

// saveDefinition saves w all-or-nothing across both backends, through Publish: the intent, the
// file, then the rows, the version and the intent's removal in one commit. A failed file write
// or commit puts the previous file back; a process that dies in between is recovered by the next
// process to read the workflow. On success w carries the version and times it was saved with.
func (h *HybridWorkflowStore) saveDefinition(ctx context.Context, w *Workflow, note string) error {
	return h.saveDefinitionAs(ctx, w, note, false)
}

// SaveWorkflowRevoking is SaveWorkflow that also removes every stored approval of the workflow
// in the same commit, so the saved definition needs a fresh one.
func (h *HybridWorkflowStore) SaveWorkflowRevoking(ctx context.Context, w *Workflow, note string) error {
	return h.saveDefinitionAs(ctx, w, note, true)
}

func (h *HybridWorkflowStore) saveDefinitionAs(ctx context.Context, w *Workflow, note string, revoke bool) error {
	if w.ID == "" {
		w.ID = newID()
	}
	// An unreadable previous copy does not block the save that replaces it.
	stored := 0
	if prev, err := h.GetWorkflow(ctx, w.ID); err == nil && prev != nil {
		stored = prev.Version
		if w.CreatedAt.IsZero() {
			w.CreatedAt = prev.CreatedAt
		}
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	published, err := h.Publish(ctx, Publication{
		WorkflowID:      w.ID,
		Origin:          "save",
		Note:            note,
		StoredVersion:   stored,
		RevokeApprovals: revoke,
		Build: func(cur *Workflow) (*Workflow, error) {
			c := *w
			c.Connections = append([]WorkflowConnection(nil), w.Connections...)
			if cur != nil {
				// The active flag the caller read earlier may be stale (another process may have switched
				// the workflow off or on since): a save never changes it. Only SetWorkflowActive does.
				c.IsActive = cur.IsActive
			} else if !approvalFrom(ctx) {
				// A new workflow starts inactive unless the operator creates it (see WithApproval): an
				// agent's save cannot make a workflow that the daemon arms by itself. Switching it on
				// afterwards needs the operator's approval too (SetWorkflowActive: RequireApproved).
				c.IsActive = false
			}
			return &c, nil
		},
	})
	if err != nil {
		return err
	}
	w.Version, w.UpdatedAt, w.IsActive = published.Version, published.UpdatedAt, published.IsActive
	for i := range w.Connections {
		w.Connections[i].ID = published.Connections[i].ID
	}
	return nil
}

// SaveWorkflowWithNote is SaveWorkflow with a note on the version it records
// (e.g. "restored from version 3").
func (h *HybridWorkflowStore) SaveWorkflowWithNote(ctx context.Context, w *Workflow, note string) error {
	return h.saveDefinition(ctx, w, note)
}

// captureVersion makes sure the workflow's current definition is recorded as
// a version and returns that version (0: the workflow is gone or has no
// SQLite row). It catches definitions changed without a full save — node and
// connection edits, a hand-edited file — and workflows saved before versions
// existed.
func (h *HybridWorkflowStore) captureVersion(ctx context.Context, workflowID string) (int, error) {
	wf, err := h.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return 0, err
	}
	v, _, err := h.sql.captureVersion(ctx, wf)
	if err != nil {
		return 0, err
	}
	// The file's own version number is not rewritten here: it is not part of the definition, and a
	// rewrite outside a publication could replace a file that is being published.
	return v, nil
}

// pinExecution records the workflow version e runs against. Best effort: an
// unpinned execution runs the workflow's current definition, as every run
// did before versions existed.
func (h *HybridWorkflowStore) pinExecution(ctx context.Context, e *WorkflowExecution) {
	v, err := h.captureVersion(ctx, e.WorkflowID)
	if err != nil || v == 0 {
		return
	}
	_ = h.sql.pinExecutionVersion(ctx, e.ID, v)
}

// ListWorkflowVersions returns a workflow's recorded versions, newest first.
func (h *HybridWorkflowStore) ListWorkflowVersions(ctx context.Context, workflowID string) ([]WorkflowVersion, error) {
	return h.sql.ListWorkflowVersions(ctx, workflowID)
}

// GetWorkflowVersion returns one recorded version, or nil, nil.
func (h *HybridWorkflowStore) GetWorkflowVersion(ctx context.Context, workflowID string, version int) (*WorkflowVersion, error) {
	return h.sql.GetWorkflowVersion(ctx, workflowID, version)
}

// PruneWorkflowVersions deletes all but the keep newest versions (sparing
// versions unfinished executions are pinned to).
func (h *HybridWorkflowStore) PruneWorkflowVersions(ctx context.Context, workflowID string, keep int) (int64, error) {
	return h.sql.PruneWorkflowVersions(ctx, workflowID, keep)
}

// ExecutionWorkflowVersion returns the version an execution is pinned to.
func (h *HybridWorkflowStore) ExecutionWorkflowVersion(ctx context.Context, executionID string) (int, error) {
	return h.sql.ExecutionWorkflowVersion(ctx, executionID)
}

// CaptureWorkflowVersion records the workflow's current definition as a
// version when it is not already the latest, and returns its version.
func (h *HybridWorkflowStore) CaptureWorkflowVersion(ctx context.Context, workflowID string) (int, error) {
	return h.captureVersion(ctx, workflowID)
}
