package workflow

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
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
	// declined remembers, per workflow, the file bytes the importer refused, so a file that stays
	// refused is not retried (under the write lock) on every read in this process.
	declined sync.Map
	// claimed: this store has made sure the legacy set is recorded (see ensureClaimed).
	claimMu sync.Mutex
	claimed bool
	// readOnly: reads never write rows (see SetReadOnly).
	readOnly bool
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

// fileWorkflows lists the file store's workflows; none when there is no file
// store. A workflow with a change in flight is left out: its file may hold a
// definition that has not committed, so the rows speak for it.
func (h *HybridWorkflowStore) fileWorkflows(ctx context.Context) ([]*Workflow, error) {
	if h.files == nil {
		return nil, nil
	}
	h.ensureClaimed(ctx)
	var kept []*Workflow
	// Only files of workflows that have rows are opened: a file with no stored copy is not a
	// workflow, and a folder of junk must not cost a parse of each on every command.
	withRows, err := h.workflowIDs(ctx)
	if err != nil {
		return nil, err
	}
	err = h.stableFileRead(ctx, func(open map[string]bool) error {
		all, err := h.files.listWorkflows(ctx, withRows)
		if err != nil {
			return err
		}
		kept = kept[:0]
		for _, wf := range all {
			if !gated(open, wf.ID) {
				kept = append(kept, wf)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h.overlayRows(ctx, kept)
}

// overlayRows makes a listing of workflow files say what the rows say about each: whose workflow it
// is, whether it is on, and its version. A file cannot give itself another owner or switch itself on
// (the daemon arms triggers from these listings). A file with no rows is left out. Listed workflows are
// copies: the file store's cache is not touched.
func (h *HybridWorkflowStore) overlayRows(ctx context.Context, files []*Workflow) ([]*Workflow, error) {
	type rowInfo struct {
		profile string
		active  bool
		version int
	}
	rows := map[string]rowInfo{}
	rs, err := h.sql.db.QueryContext(ctx, `SELECT id, profile_id, is_active, version FROM workflows`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	for rs.Next() {
		var id, profile string
		var active, version int
		if err := rs.Scan(&id, &profile, &active, &version); err != nil {
			return nil, err
		}
		rows[id] = rowInfo{profile, active != 0, version}
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	out := make([]*Workflow, 0, len(files))
	for _, wf := range files {
		r, ok := rows[wf.ID]
		if !ok {
			continue // a file with no stored copy is not a workflow (see ClaimLegacyFiles)
		}
		c := *wf
		c.ProfileID, c.IsActive, c.Version = r.profile, r.active, r.version
		out = append(out, &c)
	}
	return out, nil
}

// CreateWorkflow saves a new workflow's whole definition, all-or-nothing
// (see saveDefinition), and records it as version 1.
func (h *HybridWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	return h.saveDefinition(ctx, w, "")
}

// createsWholeDefinition: CreateWorkflow publishes the nodes and connections with the workflow.
func (h *HybridWorkflowStore) createsWholeDefinition() {}

// GetWorkflow looks in the file store first and then in SQLite, where
// imported and older workflows are kept.
func (h *HybridWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	if h.files == nil {
		return h.sql.GetWorkflow(ctx, id)
	}
	var wf *Workflow
	err := h.stableFileRead(ctx, func(open map[string]bool) (err error) {
		wf = nil
		if gated(open, id) {
			return nil
		}
		wf, err = h.reconciledFile(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	if wf == nil {
		return h.committedWorkflow(ctx, id)
	}
	return wf, nil
}

// committedWorkflow loads the rows' definition in one read transaction, so a change that commits
// meanwhile cannot give a mix of the old metadata and the new graph (nil, nil when there are no rows).
func (h *HybridWorkflowStore) committedWorkflow(ctx context.Context, id string) (*Workflow, error) {
	tx, err := h.sql.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	w, err := getWorkflowFrom(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

// committedReviewHash is the ReviewHash of the committed rows: exactly what a guarded publication
// compares (plan and commit read the rows, not the file).
func (h *HybridWorkflowStore) committedReviewHash(ctx context.Context, id string) (string, error) {
	w, err := h.committedWorkflow(ctx, id)
	if err != nil || w == nil {
		return "", err
	}
	return ReviewHash(w)
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
	if h.files == nil {
		return h.saved(ctx, w.ID, h.sql.UpdateWorkflow(ctx, w))
	}
	return h.saveDefinitionAs(ctx, w, "", false)
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

// checkOwnIDs returns ErrSharedIDs when any of w's node or connection ids
// belongs to a different workflow in SQLite.
func (s *SQLiteWorkflowStore) checkOwnIDs(ctx context.Context, w *Workflow) error {
	return checkOwnIDsOn(ctx, s.db, w)
}

// checkOwnIDsOn is checkOwnIDs over a database or a transaction.
func checkOwnIDsOn(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, w *Workflow) error {
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
		if err := db.QueryRowContext(ctx, stmt, append([]any{w.ID}, q.ids...)...).Scan(&n); err != nil {
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
	_, err := h.Publish(ctx, Publication{WorkflowID: id, Kind: PubDelete, Origin: "delete"})
	return err
}

// SetWorkflowActive flips the flag on the whole workflow as the file holds it when there is a
// file (so a hand edit is not lost), and otherwise on the committed rows. File and rows change
// in one publication.
func (h *HybridWorkflowStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	if !active {
		// The operator's kill switch: when an earlier change of this workflow is unresolved (or
		// stuck with no owner), switching it off must still work. Only the rows' flag changes; the
		// file and the open change are left exactly as they are. Switching on is never done this way.
		if done, err := h.deactivateRowsOnly(ctx, id); done || err != nil {
			return err
		}
	}
	_, err := h.Publish(ctx, Publication{
		WorkflowID:   id,
		Origin:       "active",
		KeepFileless: true,
		// Switching on arms triggers: only for a definition the operator approved (or approves with
		// this very change). Switching off is never refused for want of one.
		RequireApproved: active,
		Build: func(cur *Workflow) (*Workflow, error) {
			var next Workflow
			switch fw := h.lockedFileCopy(ctx, id); {
			case fw != nil:
				next = *fw
				if next.ProfileID == "" && cur != nil {
					next.ProfileID = cur.ProfileID
				}
			case cur != nil:
				next = *cur
			default:
				return nil, fmt.Errorf("workflow %s: %w", id, ErrWorkflowNotFound)
			}
			next.IsActive = active
			return &next, nil
		},
	})
	return err
}

// lockedFileCopy is the workflow file's definition for a publication to build on, and only when
// that file is the one that was reconciled: a plain file whose bytes are the ones the database
// recorded (the importer ran first, and either took the file in or declined it). Anything else — a
// link, a pipe, an edit that landed after the import, a file the importer declined — gives nil, and
// the publication builds on the committed rows instead of consuming bytes nobody looked at.
func (h *HybridWorkflowStore) lockedFileCopy(ctx context.Context, id string) *Workflow {
	if h.files == nil || regularFileInfo(h.files, id) == nil {
		return nil
	}
	raw, existed, err := h.files.readRaw(id)
	if err != nil || !existed {
		return nil
	}
	var known string
	if err := h.sql.db.QueryRowContext(ctx, `SELECT file_sha FROM workflow_file_state WHERE workflow_id = ?`, id).Scan(&known); err != nil || known != shaHex(append([]byte{}, raw...)) {
		return nil
	}
	wf, err := parseWorkflowFile(raw)
	if err != nil {
		return nil
	}
	return wf
}

// editBase is the definition a node or connection edit starts from: the committed rows, with the
// name, description and settings the file carries, so a hand edit of those survives. The active flag
// stays the rows'.
func (h *HybridWorkflowStore) editBase(ctx context.Context, cur *Workflow) Workflow {
	next := *cur
	if fw := h.lockedFileCopy(ctx, cur.ID); fw != nil {
		// Not the active flag: a node edit must not switch a workflow on or off (only SetWorkflowActive does).
		next.Name, next.Description, next.Settings = fw.Name, fw.Description, fw.Settings
	}
	return next
}

// fileCopy returns the file store's workflow for id, or nil when there is no
// file store, no such workflow, or the read failed.
func (h *HybridWorkflowStore) fileCopy(ctx context.Context, id string) *Workflow {
	if h.files == nil {
		return nil
	}
	var wf *Workflow
	err := h.stableFileRead(ctx, func(open map[string]bool) (err error) {
		wf = nil
		if gated(open, id) {
			return nil
		}
		wf, err = h.reconciledFile(ctx, id)
		return err
	})
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
	if h.files == nil {
		return
	}
	if existing, err := h.sql.GetWorkflow(ctx, workflowID); err != nil || existing != nil {
		return
	}
	// The file becomes rows through the importer: one transaction that does nothing while a
	// change of this workflow is open, so a file that has not committed is never backfilled.
	_, _ = h.ImportFile(ctx, workflowID, nil)
}

// publishGraph publishes workflow id with its nodes or connections replaced, through Publish:
// the file and the rows change together or not at all, and a file that cannot be written is an
// error.
func (h *HybridWorkflowStore) publishGraph(ctx context.Context, workflowID, origin string, edit func(next *Workflow)) error {
	_, err := h.Publish(ctx, Publication{
		WorkflowID:   workflowID,
		Origin:       origin,
		KeepFileless: true,
		RequireGraph: true,
		Build: func(cur *Workflow) (*Workflow, error) {
			if cur == nil {
				return nil, fmt.Errorf("workflow %s: %w", workflowID, ErrWorkflowNotFound)
			}
			next := h.editBase(ctx, cur)
			edit(&next)
			return &next, nil
		},
	})
	return err
}

// SaveWorkflowNodes replaces the workflow's nodes. A connection to a node that is gone goes with
// it, as it does in the rows.
func (h *HybridWorkflowStore) SaveWorkflowNodes(ctx context.Context, workflowID string, nodes []WorkflowNode) error {
	return h.publishGraph(ctx, workflowID, "nodes", func(next *Workflow) {
		next.Nodes = nodes
		next.Connections = connectionsAmong(next.Connections, nodes)
	})
}

// SaveWorkflowGraph replaces the workflow's nodes and connections in one change.
func (h *HybridWorkflowStore) SaveWorkflowGraph(ctx context.Context, workflowID string, nodes []WorkflowNode, conns []WorkflowConnection) error {
	return h.publishGraph(ctx, workflowID, "graph", func(next *Workflow) { next.Nodes, next.Connections = nodes, conns })
}

func connectionsAmong(conns []WorkflowConnection, nodes []WorkflowNode) []WorkflowConnection {
	have := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		have[n.ID] = true
	}
	var kept []WorkflowConnection
	for _, c := range conns {
		if have[c.SourceNodeID] && have[c.TargetNodeID] {
			kept = append(kept, c)
		}
	}
	return kept
}

func (h *HybridWorkflowStore) SaveWorkflowConnections(ctx context.Context, workflowID string, conns []WorkflowConnection) error {
	return h.publishGraph(ctx, workflowID, "connections", func(next *Workflow) { next.Connections = conns })
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

// reconciledFile is the workflow file's definition, but only once the database has taken those
// bytes in. A file changed outside the store (or put back to an older definition) is imported
// first, which records a version and removes the workflow's approvals, so nothing that reads the
// workflow to run it can see unreviewed bytes under an old approval. When the importer declines the
// file the committed rows are served (nil, nil). A workflow with no rows at all has nothing to
// protect and is served from its file, as it always was.
func (h *HybridWorkflowStore) reconciledFile(ctx context.Context, id string) (*Workflow, error) {
	h.ensureClaimed(ctx)
	// No stored copy: nothing is served from the file, so it is not even opened.
	var stored int
	if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&stored); err != nil {
		return nil, err
	}
	if stored == 0 {
		return nil, nil
	}
	raw, existed, err := h.files.readRaw(id)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, nil
	}
	sha := shaHex(append([]byte{}, raw...))
	reconciled := func() (bool, error) {
		var known string
		err := h.sql.db.QueryRowContext(ctx, `SELECT file_sha FROM workflow_file_state WHERE workflow_id = ?`, id).Scan(&known)
		switch {
		case err == nil:
			return known == sha, nil
		case errors.Is(err, sql.ErrNoRows) || isNoTable(err):
			return false, nil
		}
		return false, err
	}
	ok, err := reconciled()
	if err != nil {
		return nil, err
	}
	if !ok {
		if d, _ := h.declined.Load(id); d == sha {
			return nil, nil
		}
		var n int
		if err := h.sql.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflows WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, err
		}
		if n == 0 {
			// No stored copy: nothing is served from the file alone (older versions' files were
			// taken in when the store was first used; later ones wait to be adopted).
			return nil, nil
		}
		if n > 0 && h.readOnly {
			return nil, nil // not reconciled, and this store may not write: the rows speak
		}
		if n > 0 {
			if _, err := h.ImportFile(ctx, id, nil); err != nil && !errors.Is(err, ErrSharedIDs) {
				return nil, err
			}
			if ok, err = reconciled(); err != nil {
				return nil, err
			}
			if !ok {
				h.declined.Store(id, sha)
				return nil, nil
			}
		}
	}
	h.declined.Delete(id)
	wf, err := parseWorkflowFile(raw)
	if err != nil || wf.ID != id {
		return nil, err
	}
	// What the file cannot decide comes from the rows: whose workflow it is, whether it is on, and
	// its version. (A workflow with no rows has none of these on record.)
	var active int
	var profile string
	var version int
	switch err := h.sql.db.QueryRowContext(ctx, `SELECT profile_id, is_active, version FROM workflows WHERE id = ?`, id).Scan(&profile, &active, &version); {
	case err == nil:
		wf.ProfileID, wf.IsActive, wf.Version = profile, active != 0, version
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	return wf, nil
}

// UnfinishedChange says what is wrong with a workflow's change record: "unresolved", "stuck" (a
// pending change whose owner is gone) or "" (nothing, or a change still being made).
func (h *HybridWorkflowStore) UnfinishedChange(ctx context.Context, id string) (string, error) {
	in, err := scanIntent(h.sql.db.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM workflow_publications WHERE workflow_id = ?`, id))
	switch {
	case errors.Is(err, sql.ErrNoRows) || (err != nil && isNoTable(err)):
		return "", nil
	case err != nil:
		return "", err
	case in.State == "unresolved":
		return "unresolved", nil
	case in.State == "pending" && !in.live():
		return "stuck", nil
	}
	return "", nil
}

// deactivateRowsOnly switches the rows' active flag off under the publication lock when the
// workflow has an unresolved or stuck change; done says whether it did.
func (h *HybridWorkflowStore) deactivateRowsOnly(ctx context.Context, id string) (done bool, err error) {
	if h.files == nil {
		return false, nil
	}
	state, err := h.UnfinishedChange(ctx, id)
	if err != nil || state == "" {
		return false, err
	}
	tx, txDone, err := h.beginDurable(ctx)
	if err != nil {
		return false, err
	}
	defer txDone()
	defer tx.Rollback()
	if err := lockPublication(ctx, tx, id); err != nil {
		return false, err
	}
	// The change may have been settled while the lock was awaited: look again under it.
	in, err := scanIntent(tx.QueryRowContext(ctx, `SELECT `+intentColumns+` FROM workflow_publications WHERE workflow_id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && in.State == "pending" && in.live()) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET is_active = 0, updated_at = ? WHERE id = ?`, time.Now().UTC(), id); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// workflowIDs is the set of ids that have rows.
func (h *HybridWorkflowStore) workflowIDs(ctx context.Context) (map[string]bool, error) {
	rs, err := h.sql.db.QueryContext(ctx, `SELECT id FROM workflows`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	out := map[string]bool{}
	for rs.Next() {
		var id string
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rs.Err()
}

// SetReadOnly makes the store's reads write nothing: it does not take in the files older versions
// left, and a file edited outside the store is not imported by a read (the committed rows are served
// until some other command imports it). For paths that are polled, like the summary.
func (h *HybridWorkflowStore) SetReadOnly() {
	h.claimMu.Lock()
	defer h.claimMu.Unlock()
	h.readOnly, h.claimed = true, true
}
