package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// HybridWorkflowStore delegates workflow-definition operations to a
// WorkflowFileStore (JSON files) and all execution/credential operations to a
// SQLiteWorkflowStore.
//
// A nil file store means workflow definitions live in SQLite alone. This is
// the store both the CLI and the desktop app go through.
type HybridWorkflowStore struct {
	files   *WorkflowFileStore
	sql     *SQLiteWorkflowStore
	onSaved func(ctx context.Context, workflowID string)
}

// SetOnSaved registers fn to run after every successful save of a
// workflow's definition or nodes (the CLI re-derives org grant tiers).
func (h *HybridWorkflowStore) SetOnSaved(fn func(ctx context.Context, workflowID string)) {
	h.onSaved = fn
}

// saved runs the OnSaved hook when err is nil, and returns err.
func (h *HybridWorkflowStore) saved(ctx context.Context, workflowID string, err error) error {
	if err == nil && h.onSaved != nil && workflowID != "" {
		h.onSaved(ctx, workflowID)
	}
	return err
}

// NewHybridWorkflowStore builds a HybridWorkflowStore over the two backends;
// pass nil files to keep definitions in SQLite only.
func NewHybridWorkflowStore(files *WorkflowFileStore, sql *SQLiteWorkflowStore) *HybridWorkflowStore {
	return &HybridWorkflowStore{files: files, sql: sql}
}

// ---------------------------------------------------------------------------
// Workflow CRUD — the file store wins, SQLite covers what it lacks
// ---------------------------------------------------------------------------

// persist writes w through whichever backend holds definitions. With a file
// store it saves the file and mirrors the result into SQLite; without one it
// runs sqlWrite against SQLite directly. The OnSaved hook fires on success.
func (h *HybridWorkflowStore) persist(ctx context.Context, w *Workflow, sqlWrite func(context.Context, *Workflow) error) error {
	if h.files == nil {
		return h.saved(ctx, w.ID, sqlWrite(ctx, w))
	}
	if err := h.files.SaveWorkflow(ctx, w); err != nil {
		return err
	}
	return h.saved(ctx, w.ID, h.mirrorToSQL(ctx, w))
}

// fileWorkflows lists the file store's workflows; none when there is no file
// store.
func (h *HybridWorkflowStore) fileWorkflows(ctx context.Context) ([]*Workflow, error) {
	if h.files == nil {
		return nil, nil
	}
	return h.files.ListWorkflows(ctx)
}

// CreateWorkflow saves a new workflow's whole definition, all-or-nothing
// (see saveDefinition), and records it as version 1.
func (h *HybridWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	return h.saveDefinition(ctx, w, "")
}

// GetWorkflow looks in the file store first and then in SQLite, where
// imported and older workflows are kept.
func (h *HybridWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	if h.files == nil {
		return h.sql.GetWorkflow(ctx, id)
	}
	wf, err := h.files.GetWorkflow(ctx, id)
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return h.sql.GetWorkflow(ctx, id)
	}
	return wf, nil
}

// ListWorkflows returns the profile's file-store workflows followed by the
// profile's SQLite workflows the files don't cover. An empty profileID is
// ErrNoProfile.
func (h *HybridWorkflowStore) ListWorkflows(ctx context.Context, profileID string) ([]Workflow, error) {
	if profileID == "" {
		return nil, fmt.Errorf("listing workflows: %w", ErrNoProfile)
	}
	fromFiles, err := h.fileWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	fromFiles = filterFileWorkflows(fromFiles, profileID)
	fromSQL, err := h.sql.ListWorkflows(ctx, profileID)
	if err != nil {
		return nil, err
	}
	return mergeWorkflowLists(fromFiles, fromSQL), nil
}

// ListAllProfilesWorkflows is ListWorkflows across every profile, for the
// daemon engine, `workflow migrate` and the all-profiles listings.
func (h *HybridWorkflowStore) ListAllProfilesWorkflows(ctx context.Context) ([]Workflow, error) {
	fromFiles, err := h.fileWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	fromSQL, err := h.sql.ListAllProfilesWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	return mergeWorkflowLists(fromFiles, fromSQL), nil
}

// filterFileWorkflows keeps the file workflows owned by profileID. A file
// without a profile belongs to no profile's list (it is the legacy state the
// startup stamp removes).
func filterFileWorkflows(files []*Workflow, profileID string) []*Workflow {
	out := make([]*Workflow, 0, len(files))
	for _, wf := range files {
		if wf.ProfileID == profileID {
			out = append(out, wf)
		}
	}
	return out
}

// mergeWorkflowLists puts the file workflows first, then the SQLite ones the
// files don't cover.
func mergeWorkflowLists(fromFiles []*Workflow, fromSQL []Workflow) []Workflow {
	covered := make(map[string]struct{}, len(fromFiles))
	var out []Workflow
	for _, wf := range fromFiles {
		covered[wf.ID] = struct{}{}
		out = append(out, *wf)
	}
	for _, wf := range fromSQL {
		if _, dup := covered[wf.ID]; !dup {
			out = append(out, wf)
		}
	}
	return out
}

// NodeCounts returns each workflow's node count, keyed by workflow id,
// following the same precedence as ListWorkflows: a workflow present in the
// file store is the file store's, and SQLite supplies the rest. File-store
// workflows are parsed whole, so their count is already in hand; only the
// SQLite half needs a query.
func (h *HybridWorkflowStore) NodeCounts(ctx context.Context, profileID string) (map[string]int, error) {
	if profileID == "" {
		return nil, fmt.Errorf("counting workflow nodes: %w", ErrNoProfile)
	}
	counts := make(map[string]int)
	fromFiles, err := h.fileWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	for _, wf := range filterFileWorkflows(fromFiles, profileID) {
		counts[wf.ID] = len(wf.Nodes)
	}
	sqlCounts, err := h.sql.NodeCounts(ctx, profileID)
	if err != nil {
		return nil, err
	}
	for id, n := range sqlCounts {
		if _, fromFile := counts[id]; !fromFile {
			counts[id] = n
		}
	}
	return counts, nil
}

func (h *HybridWorkflowStore) UpdateWorkflow(ctx context.Context, w *Workflow) error {
	return h.persist(ctx, w, h.sql.UpdateWorkflow)
}

// SaveWorkflow writes a workflow to the file store (create or update) and
// mirrors it — metadata, nodes, connections — to SQLite, which executions,
// profile scoping and `workflow run --json` read. The save is all-or-nothing
// across both (see saveDefinition) and records a version when the
// definition changed.
func (h *HybridWorkflowStore) SaveWorkflow(ctx context.Context, w *Workflow) error {
	return h.saveDefinition(ctx, w, "")
}

// ErrSharedIDs reports a workflow whose node or connection ids belong to
// another workflow in SQLite; such a workflow is not mirrored.
var ErrSharedIDs = errors.New("workflow shares node or connection ids with another workflow")

// mirrorToSQL makes SQLite's copy of w match the file just written: the
// workflow row (created, or its name/description/active flag updated; the
// profile and version are left alone), then nodes and connections. The
// file stays canonical. A workflow whose node/connection ids are owned by
// another workflow is skipped (SaveWorkflowNodes' upsert would move them);
// the save itself still succeeds. The caller's w is not modified.
func (h *HybridWorkflowStore) mirrorToSQL(ctx context.Context, w *Workflow) error {
	if h.sql == nil || w == nil || w.ID == "" {
		return nil
	}
	if err := h.sql.checkOwnIDs(ctx, w); err != nil {
		if errors.Is(err, ErrSharedIDs) {
			return nil
		}
		return err
	}
	var n int
	if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, w.ID).Scan(&n); err != nil {
		return fmt.Errorf("mirror workflow %s: %w", w.ID, err)
	}
	if n == 0 {
		stub := *w
		stub.Nodes, stub.Connections = nil, nil
		if err := h.sql.CreateWorkflow(ctx, &stub); err != nil {
			return fmt.Errorf("mirror workflow %s: %w", w.ID, err)
		}
	} else if _, err := h.sql.db.ExecContext(ctx,
		`UPDATE workflows SET name = ?, description = ?, is_active = ?, updated_at = ? WHERE id = ?`,
		w.Name, w.Description, boolToInt(w.IsActive), time.Now().UTC(), w.ID); err != nil {
		return fmt.Errorf("mirror workflow %s: %w", w.ID, err)
	}
	saveWorkflowSettings(ctx, h.sql.db, w)
	nodes := append([]WorkflowNode(nil), w.Nodes...)
	if err := h.sql.SaveWorkflowNodes(ctx, w.ID, nodes); err != nil {
		return fmt.Errorf("mirror workflow %s nodes: %w", w.ID, err)
	}
	conns := append([]WorkflowConnection(nil), w.Connections...)
	if err := h.sql.SaveWorkflowConnections(ctx, w.ID, conns); err != nil {
		return fmt.Errorf("mirror workflow %s connections: %w", w.ID, err)
	}
	return nil
}

// checkOwnIDs returns ErrSharedIDs when any of w's node or connection ids
// belongs to a different workflow in SQLite.
func (s *SQLiteWorkflowStore) checkOwnIDs(ctx context.Context, w *Workflow) error {
	var nodeIDs, connIDs []any
	for _, n := range w.Nodes {
		if n.ID != "" {
			nodeIDs = append(nodeIDs, n.ID)
		}
	}
	for _, c := range w.Connections {
		if c.ID != "" {
			connIDs = append(connIDs, c.ID)
		}
	}
	for _, q := range []struct {
		table string
		ids   []any
	}{{"workflow_nodes", nodeIDs}, {"workflow_connections", connIDs}} {
		if len(q.ids) == 0 {
			continue
		}
		stmt := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE workflow_id != ? AND id IN (%s)`, q.table,
			strings.TrimSuffix(strings.Repeat("?,", len(q.ids)), ","))
		var n int
		if err := s.db.QueryRowContext(ctx, stmt, append([]any{w.ID}, q.ids...)...).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %s (%d in %s)", ErrSharedIDs, w.ID, n, q.table)
		}
	}
	return nil
}

// DeleteWorkflow removes a workflow from both stores. "Already gone" from
// either side is treated as success (the caller wanted it gone; it is) —
// WorkflowFileStore.DeleteWorkflow already returns nil for a missing file,
// and SQLiteWorkflowStore.DeleteWorkflow wraps ErrWorkflowNotFound for zero
// rows affected. Any OTHER error (permission denied, disk full, a genuine
// DB error) is propagated, not swallowed: this method previously discarded
// every error unconditionally, which made it unsafe to use as a
// compensating cleanup action — a caller relying on its return value to
// know whether cleanup actually happened would be told "success" even when
// the workflow (or its rows) are still sitting there.
func (h *HybridWorkflowStore) DeleteWorkflow(ctx context.Context, id string) error {
	var fileErr, sqlErr error
	if h.files != nil {
		if err := h.files.DeleteWorkflow(ctx, id); err != nil {
			fileErr = fmt.Errorf("file store: %w", err)
		}
	}
	if err := h.sql.DeleteWorkflow(ctx, id); err != nil && !errors.Is(err, ErrWorkflowNotFound) {
		sqlErr = fmt.Errorf("sql store: %w", err)
	}
	return errors.Join(fileErr, sqlErr)
}

// SetWorkflowActive flips the flag on the file-store copy when there is one
// (mirroring it to SQLite), and otherwise on the SQLite row alone.
func (h *HybridWorkflowStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	if wf := h.fileCopy(ctx, id); wf != nil {
		wf.IsActive = active
		if err := h.files.SaveWorkflow(ctx, wf); err != nil {
			return err
		}
		return h.mirrorToSQL(ctx, wf)
	}
	return h.sql.SetWorkflowActive(ctx, id, active)
}

// fileCopy returns the file store's workflow for id, or nil when there is no
// file store, no such workflow, or the read failed.
func (h *HybridWorkflowStore) fileCopy(ctx context.Context, id string) *Workflow {
	if h.files == nil {
		return nil
	}
	wf, err := h.files.GetWorkflow(ctx, id)
	if err != nil {
		return nil
	}
	return wf
}

// ---------------------------------------------------------------------------
// Node / Connection save — SQLite only (executions need them there)
// ---------------------------------------------------------------------------

// ensureSQLWorkflow guarantees a minimal metadata row exists in SQLite for
// workflowID, mirroring it from the file store if necessary. Nodes,
// connections, and executions all have hard FK references to workflows(id)
// in SQLite even when the workflow's canonical definition lives only in the
// file store (e.g. created via the CLI's `workflow create`).
func (h *HybridWorkflowStore) ensureSQLWorkflow(ctx context.Context, workflowID string) {
	existing, err := h.sql.GetWorkflow(ctx, workflowID)
	if err != nil || existing != nil {
		return
	}
	if h.files == nil {
		return
	}
	wf, ferr := h.files.GetWorkflow(ctx, workflowID)
	if ferr != nil || wf == nil {
		return
	}
	// Insert a minimal stub row — only metadata, no nodes/connections.
	stub := *wf
	stub.Nodes = nil
	stub.Connections = nil
	_ = h.sql.CreateWorkflow(ctx, &stub)
}

// syncFileNodes mirrors the current SQL-stored nodes/connections for
// workflowID back onto the file-store copy. GetWorkflow prefers the file
// store, so without this, nodes/connections written via SaveWorkflowNodes/
// SaveWorkflowConnections would be invisible to every read path.
func (h *HybridWorkflowStore) syncFileNodes(ctx context.Context, workflowID string) {
	if h.files == nil {
		return
	}
	// The same lock a guarded save holds from publishing its file to its commit, so this
	// copy of the rows is never older than a file already published.
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	wf, err := h.files.GetWorkflow(ctx, workflowID)
	if err != nil || wf == nil {
		return
	}
	sqlWF, err := h.sql.GetWorkflow(ctx, workflowID)
	if err != nil || sqlWF == nil {
		return
	}
	wf.Nodes = sqlWF.Nodes
	wf.Connections = sqlWF.Connections
	_ = h.files.saveWorkflow(ctx, wf)
}

func (h *HybridWorkflowStore) SaveWorkflowNodes(ctx context.Context, workflowID string, nodes []WorkflowNode) error {
	h.ensureSQLWorkflow(ctx, workflowID)
	if err := h.sql.SaveWorkflowNodes(ctx, workflowID, nodes); err != nil {
		return err
	}
	h.syncFileNodes(ctx, workflowID)
	_, _ = h.captureVersion(ctx, workflowID) // best effort: CreateExecution captures again
	return h.saved(ctx, workflowID, nil)
}

func (h *HybridWorkflowStore) SaveWorkflowConnections(ctx context.Context, workflowID string, conns []WorkflowConnection) error {
	h.ensureSQLWorkflow(ctx, workflowID)
	if err := h.sql.SaveWorkflowConnections(ctx, workflowID, conns); err != nil {
		return err
	}
	h.syncFileNodes(ctx, workflowID)
	_, _ = h.captureVersion(ctx, workflowID) // best effort: CreateExecution captures again
	return nil
}

// ---------------------------------------------------------------------------
// Executions — always SQLite
// ---------------------------------------------------------------------------

// CreateExecution inserts the execution and pins it to the workflow's
// current definition version, which handleExecution runs (and a WAITING
// run resumes) against.
func (h *HybridWorkflowStore) CreateExecution(ctx context.Context, e *WorkflowExecution) error {
	h.ensureSQLWorkflow(ctx, e.WorkflowID)
	if err := h.sql.CreateExecution(ctx, e); err != nil {
		return err
	}
	h.pinExecution(ctx, e)
	return nil
}

func (h *HybridWorkflowStore) GetExecution(ctx context.Context, id string) (*WorkflowExecution, error) {
	return h.sql.GetExecution(ctx, id)
}

func (h *HybridWorkflowStore) ListExecutions(ctx context.Context, workflowID string, limit int) ([]WorkflowExecution, error) {
	return h.sql.ListExecutions(ctx, workflowID, limit)
}

func (h *HybridWorkflowStore) UpdateExecutionStatus(ctx context.Context, id string, status string, errMsg string) error {
	return h.sql.UpdateExecutionStatus(ctx, id, status, errMsg)
}

func (h *HybridWorkflowStore) StartQueuedExecution(ctx context.Context, id string) (bool, error) {
	return h.sql.StartQueuedExecution(ctx, id)
}

func (h *HybridWorkflowStore) SetExecutionStarted(ctx context.Context, id string) error {
	return h.sql.SetExecutionStarted(ctx, id)
}

func (h *HybridWorkflowStore) SetExecutionFinished(ctx context.Context, id string, status string, errMsg string) error {
	return h.sql.SetExecutionFinished(ctx, id, status, errMsg)
}

func (h *HybridWorkflowStore) CreateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	return h.sql.CreateExecutionNode(ctx, en)
}

func (h *HybridWorkflowStore) UpdateExecutionNode(ctx context.Context, en *WorkflowExecutionNode) error {
	return h.sql.UpdateExecutionNode(ctx, en)
}

func (h *HybridWorkflowStore) SetExecutionNodeFinished(ctx context.Context, id string, status string, outputItems []Item, errMsg string) error {
	return h.sql.SetExecutionNodeFinished(ctx, id, status, outputItems, errMsg)
}

// ---------------------------------------------------------------------------
// Maintenance — SQLite
// ---------------------------------------------------------------------------

func (h *HybridWorkflowStore) RecoverStaleExecutions(ctx context.Context) error {
	return h.sql.RecoverStaleExecutions(ctx)
}

func (h *HybridWorkflowStore) ReapStaleRunningExecutions(ctx context.Context, olderThan time.Time) error {
	return h.sql.ReapStaleRunningExecutions(ctx, olderThan)
}

func (h *HybridWorkflowStore) CancelQueuedExecution(ctx context.Context, id string) (bool, error) {
	return h.sql.CancelQueuedExecution(ctx, id)
}

func (h *HybridWorkflowStore) SetExecutionWaiting(ctx context.Context, id string, resumeState string) error {
	return h.sql.SetExecutionWaiting(ctx, id, resumeState)
}

func (h *HybridWorkflowStore) ResumeWaitingExecution(ctx context.Context, id string) (bool, error) {
	return h.sql.ResumeWaitingExecution(ctx, id)
}

func (h *HybridWorkflowStore) ListResumableExecutions(ctx context.Context) ([]string, error) {
	return h.sql.ListResumableExecutions(ctx)
}

func (h *HybridWorkflowStore) ListAdoptableExecutions(ctx context.Context) ([]string, error) {
	return h.sql.ListAdoptableExecutions(ctx)
}

func (h *HybridWorkflowStore) ClaimQueuedExecution(ctx context.Context, id string) (bool, error) {
	return h.sql.ClaimQueuedExecution(ctx, id)
}

func (h *HybridWorkflowStore) PruneExecutions(ctx context.Context, workflowID string, keepCount int) error {
	return h.sql.PruneExecutions(ctx, workflowID, keepCount)
}

// RawDB delegates to the SQLite store's underlying DB.
func (h *HybridWorkflowStore) RawDB() *sql.DB { return h.sql.RawDB() }
