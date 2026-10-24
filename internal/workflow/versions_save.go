package workflow

import (
	"context"
	"errors"
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

	now := time.Now().UTC()
	created := w.CreatedAt
	if created.IsZero() {
		created = now
	}
	profileID := w.ProfileID
	if profileID == "" {
		return fmt.Errorf("saving workflow %s: %w", w.ID, ErrNoProfile)
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
		return fmt.Errorf("saving workflow %s: %w", w.ID, err)
	}
	saveWorkflowSettings(ctx, tx, w)
	var rowVersion int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM workflows WHERE id = ?`, w.ID).Scan(&rowVersion); err != nil {
		return fmt.Errorf("reading version of workflow %s: %w", w.ID, err)
	}
	if mirrorGraph {
		if err := saveNodesTx(ctx, tx, w.ID, append([]WorkflowNode(nil), w.Nodes...)); err != nil {
			return fmt.Errorf("saving workflow %s nodes: %w", w.ID, err)
		}
		if err := saveConnectionsTx(ctx, tx, w.ID, append([]WorkflowConnection(nil), w.Connections...)); err != nil {
			return fmt.Errorf("saving workflow %s connections: %w", w.ID, err)
		}
	}
	v, _, err := recordVersionTx(ctx, tx, w, max(storedVersion, rowVersion), false, note)
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

// saveDefinition saves w all-or-nothing across both backends. The SQLite
// transaction (row, nodes, connections, version) is built first; the file —
// the canonical copy — is written as its last step, and the transaction
// commits only after the file is on disk. A failed file write rolls the
// transaction back; a failed commit puts the previous file back. Either way
// the stores keep the definition they had.
func (h *HybridWorkflowStore) saveDefinition(ctx context.Context, w *Workflow, note string) error {
	if w.ID == "" {
		w.ID = newID()
	}
	// Give connections their ids now, so the file, the SQLite rows and the
	// version snapshot all name them the same.
	for i := range w.Connections {
		if w.Connections[i].ID == "" {
			w.Connections[i].ID = newID()
		}
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
	mirrorGraph := true
	if err := h.sql.checkOwnIDs(ctx, w); err != nil {
		if !errors.Is(err, ErrSharedIDs) {
			return err
		}
		mirrorGraph = false
	}

	var restoreFile func() error
	var writeFile func() error
	if h.files != nil {
		writeFile = func() error {
			prevData, existed, err := h.files.readRaw(w.ID)
			if err != nil {
				return err
			}
			if err := h.files.SaveWorkflow(ctx, w); err != nil {
				return err
			}
			restoreFile = func() error { return h.files.restoreRaw(w.ID, prevData, existed) }
			return nil
		}
	}
	err := h.sql.saveDefinition(ctx, w, mirrorGraph, stored, note, writeFile)
	if err != nil && restoreFile != nil {
		if rerr := restoreFile(); rerr != nil {
			err = errors.Join(err, fmt.Errorf("restoring the previous workflow file: %w", rerr))
		}
	}
	return h.saved(ctx, w.ID, err)
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
	v, created, err := h.sql.captureVersion(ctx, wf)
	if err != nil {
		return 0, err
	}
	// Keep the file's version in step with the history, as a save does —
	// unless the file changed again meanwhile (its own save numbers it).
	if created && wf.Version != v {
		if fileWF := h.fileCopy(ctx, workflowID); fileWF != nil && sameDefinition(fileWF, wf) {
			fileWF.Version = v
			_ = h.files.SaveWorkflow(ctx, fileWF)
		}
	}
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
