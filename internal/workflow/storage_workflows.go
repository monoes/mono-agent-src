package workflow

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/sqltime"
)

// ---------------------------------------------------------------------------
// Workflow CRUD
// ---------------------------------------------------------------------------

// CreateWorkflow inserts a new workflow row. If w.ID is empty a UUID is generated.
// w.Nodes and w.Connections are ignored here; use SaveWorkflowNodes /
// SaveWorkflowConnections to persist the graph.
func (s *SQLiteWorkflowStore) CreateWorkflow(ctx context.Context, w *Workflow) error {
	if w.ID == "" {
		w.ID = newID()
	}
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	if w.Version == 0 {
		w.Version = 1
	}
	if w.ProfileID == "" {
		return fmt.Errorf("creating workflow %s: %w", w.ID, ErrNoProfile)
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO workflows (id, name, description, is_active, version, profile_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Description, boolToInt(w.IsActive), w.Version, w.ProfileID,
		w.CreatedAt, w.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating workflow %s: %w", w.ID, err)
	}
	saveWorkflowSettings(ctx, s.db, w)
	return nil
}

// GetWorkflow retrieves a workflow by ID along with its nodes and connections.
// Returns nil, nil when not found.
func (s *SQLiteWorkflowStore) GetWorkflow(ctx context.Context, id string) (*Workflow, error) {
	w := &Workflow{}
	var isActive int
	var createdAt, updatedAt sqliteTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, is_active, version, profile_id, created_at, updated_at
		FROM workflows WHERE id = ?`, id,
	).Scan(&w.ID, &w.Name, &w.Description, &isActive, &w.Version, &w.ProfileID, &createdAt, &updatedAt)
	w.CreatedAt = createdAt.Time
	w.UpdatedAt = updatedAt.Time
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting workflow %s: %w", id, err)
	}
	w.IsActive = isActive != 0
	loadWorkflowSettings(ctx, s.db, w)

	nodes, err := s.loadWorkflowNodes(ctx, id)
	if err != nil {
		return nil, err
	}
	w.Nodes = nodes

	conns, err := s.loadWorkflowConnections(ctx, id)
	if err != nil {
		return nil, err
	}
	w.Connections = conns

	return w, nil
}

// ListWorkflows returns the profile's workflows ordered by created_at DESC.
// An empty profileID is ErrNoProfile: "every profile" is spelled
// ListAllProfilesWorkflows. Nodes and Connections are not populated; call
// GetWorkflow for full detail.
// workflowsNewestFirst orders workflows by when they were created: created_at
// holds both a time.Time's text and RFC3339 (sqltime), which text order mixes.
var workflowsNewestFirst = sqltime.SortKey("created_at") + " DESC, rowid DESC"

func (s *SQLiteWorkflowStore) ListWorkflows(ctx context.Context, profileID string) ([]Workflow, error) {
	if profileID == "" {
		return nil, fmt.Errorf("listing workflows: %w", ErrNoProfile)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, is_active, version, profile_id, created_at, updated_at
		FROM workflows WHERE profile_id = ? ORDER BY `+workflowsNewestFirst, profileID)
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	return s.scanWorkflowList(ctx, rows)
}

// ListAllProfilesWorkflows returns every profile's workflows, created_at
// DESC. It is only for code that deliberately spans profiles (the daemon
// engine, `workflow migrate`, the all-profiles listings).
func (s *SQLiteWorkflowStore) ListAllProfilesWorkflows(ctx context.Context) ([]Workflow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, description, is_active, version, profile_id, created_at, updated_at
		FROM workflows ORDER BY `+workflowsNewestFirst)
	if err != nil {
		return nil, fmt.Errorf("listing workflows: %w", err)
	}
	return s.scanWorkflowList(ctx, rows)
}

func (s *SQLiteWorkflowStore) scanWorkflowList(ctx context.Context, rows *sql.Rows) ([]Workflow, error) {
	defer rows.Close()

	var out []Workflow
	for rows.Next() {
		w := Workflow{}
		var isActive int
		var createdAt, updatedAt sqliteTime
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &isActive, &w.Version,
			&w.ProfileID, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scanning workflow row: %w", err)
		}
		w.CreatedAt = createdAt.Time
		w.UpdatedAt = updatedAt.Time
		w.IsActive = isActive != 0
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	loadWorkflowsSettings(ctx, s.db, out)
	return out, nil
}

// NodeCounts returns each workflow's node count, keyed by workflow id, for
// profileID. An empty profileID is ErrNoProfile.
//
// ListWorkflows deliberately leaves Nodes unpopulated so listing stays cheap,
// which left callers with no way to show a count — the GUI's workflow list
// rendered "0 nodes" for every workflow, including ones with nodes. This
// answers that in one grouped query rather than a GetWorkflow per row.
func (s *SQLiteWorkflowStore) NodeCounts(ctx context.Context, profileID string) (map[string]int, error) {
	const base = `
		SELECT w.id, COUNT(n.id)
		FROM workflows w
		LEFT JOIN workflow_nodes n ON n.workflow_id = w.id`
	if profileID == "" {
		return nil, fmt.Errorf("counting workflow nodes: %w", ErrNoProfile)
	}
	rows, err := s.db.QueryContext(ctx, base+`
		WHERE w.profile_id = ?
		GROUP BY w.id`, profileID)
	if err != nil {
		return nil, fmt.Errorf("counting workflow nodes: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scanning node count row: %w", err)
		}
		counts[id] = n
	}
	return counts, rows.Err()
}

// UpdateWorkflow updates the mutable fields of an existing workflow.
// Nodes and Connections are not touched; use SaveWorkflowNodes /
// SaveWorkflowConnections for that.
func (s *SQLiteWorkflowStore) UpdateWorkflow(ctx context.Context, w *Workflow) error {
	w.UpdatedAt = time.Now().UTC()
	w.Version++

	_, err := s.db.ExecContext(ctx, `
		UPDATE workflows
		SET name = ?, description = ?, is_active = ?, version = ?, updated_at = ?
		WHERE id = ?`,
		w.Name, w.Description, boolToInt(w.IsActive), w.Version, w.UpdatedAt, w.ID,
	)
	if err != nil {
		return fmt.Errorf("updating workflow %s: %w", w.ID, err)
	}
	saveWorkflowSettings(ctx, s.db, w)
	return nil
}

// DeleteWorkflow removes a workflow and, via ON DELETE CASCADE, all associated
// nodes, connections, and executions.
func (s *SQLiteWorkflowStore) DeleteWorkflow(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, "DELETE FROM workflows WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting workflow %s: %w", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		// Wrapped in the package sentinel (not a bare fmt.Errorf) so callers
		// like HybridWorkflowStore.DeleteWorkflow can distinguish "already
		// gone, nothing to do" from a genuine deletion failure via errors.Is
		// instead of matching on this exact message text.
		return fmt.Errorf("%w: %s", ErrWorkflowNotFound, id)
	}
	return nil
}

// SetWorkflowActive toggles the is_active flag on a workflow.
func (s *SQLiteWorkflowStore) SetWorkflowActive(ctx context.Context, id string, active bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE workflows SET is_active = ?, updated_at = ? WHERE id = ?",
		boolToInt(active), time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("setting workflow %s active=%v: %w", id, active, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Nodes & Connections
// ---------------------------------------------------------------------------

// SaveWorkflowNodes upserts all supplied nodes for a workflow atomically and
// deletes only the nodes that are no longer present. Connections of
// surviving nodes are untouched; connections of deleted nodes cascade via
// the source/target FKs as before.
func (s *SQLiteWorkflowStore) SaveWorkflowNodes(ctx context.Context, workflowID string, nodes []WorkflowNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning save-nodes transaction: %w", err)
	}
	if err := saveNodesTx(ctx, tx, workflowID, nodes); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing save-nodes transaction: %w", err)
	}
	return nil
}

// saveNodesTx is SaveWorkflowNodes inside the caller's transaction; the
// caller rolls back on error.
func saveNodesTx(ctx context.Context, tx *sql.Tx, workflowID string, nodes []WorkflowNode) error {
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO workflow_nodes
			(id, workflow_id, node_type, name, config, position_x, position_y, disabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			workflow_id = excluded.workflow_id,
			node_type   = excluded.node_type,
			name        = excluded.name,
			config      = excluded.config,
			position_x  = excluded.position_x,
			position_y  = excluded.position_y,
			disabled    = excluded.disabled,
			updated_at  = excluded.updated_at`)
	if err != nil {
		return fmt.Errorf("preparing node upsert: %w", err)
	}
	defer stmt.Close()

	args := make([]interface{}, 0, len(nodes)+1)
	args = append(args, workflowID)
	now := time.Now().UTC()
	for i := range nodes {
		n := &nodes[i]
		if n.ID == "" {
			n.ID = newID()
		}
		n.WorkflowID = workflowID
		if err := n.MarshalConfig(); err != nil {
			return fmt.Errorf("marshalling config for node %s: %w", n.ID, err)
		}
		n.CreatedAt = now
		n.UpdatedAt = now

		if _, err := stmt.ExecContext(ctx,
			n.ID, n.WorkflowID, n.Type, n.Name, n.ConfigRaw,
			n.PositionX, n.PositionY, boolToInt(n.Disabled),
			n.CreatedAt, n.UpdatedAt,
		); err != nil {
			return fmt.Errorf("upserting node %s: %w", n.ID, err)
		}
		args = append(args, n.ID)
	}

	// Delete only genuinely-removed nodes; their connections cascade via FK.
	var delSQL string
	if len(nodes) == 0 {
		delSQL = "DELETE FROM workflow_nodes WHERE workflow_id = ?"
	} else {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",")
		delSQL = "DELETE FROM workflow_nodes WHERE workflow_id = ? AND id NOT IN (" + placeholders + ")"
	}
	if _, err := tx.ExecContext(ctx, delSQL, args...); err != nil {
		return fmt.Errorf("deleting removed nodes for workflow %s: %w", workflowID, err)
	}
	return nil
}

// SaveWorkflowConnections replaces all connections for a workflow atomically.
// Existing connections are deleted and the supplied slice is inserted fresh.
func (s *SQLiteWorkflowStore) SaveWorkflowConnections(ctx context.Context, workflowID string, conns []WorkflowConnection) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning save-connections transaction: %w", err)
	}
	if err := saveConnectionsTx(ctx, tx, workflowID, conns); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing save-connections transaction: %w", err)
	}
	return nil
}

// saveConnectionsTx is SaveWorkflowConnections inside the caller's
// transaction; the caller rolls back on error.
func saveConnectionsTx(ctx context.Context, tx *sql.Tx, workflowID string, conns []WorkflowConnection) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM workflow_connections WHERE workflow_id = ?", workflowID); err != nil {
		return fmt.Errorf("deleting old connections for workflow %s: %w", workflowID, err)
	}

	// Validate node references up front, in the same transaction the insert
	// below would otherwise rely on: without this, a connection naming an
	// unknown node id (a stale id after a node was renamed/removed, a typo
	// in a hand-edited or generated workflow JSON, etc.) surfaces only as
	// the SQLite foreign-key error itself ("constraint failed: FOREIGN KEY
	// constraint failed (787)") — accurate but useless for finding which
	// connection or which node id is actually wrong.
	nodeRows, err := tx.QueryContext(ctx, "SELECT id FROM workflow_nodes WHERE workflow_id = ?", workflowID)
	if err != nil {
		return fmt.Errorf("loading node ids for workflow %s: %w", workflowID, err)
	}
	validNodeIDs := make(map[string]bool)
	for nodeRows.Next() {
		var id string
		if err := nodeRows.Scan(&id); err != nil {
			nodeRows.Close()
			return fmt.Errorf("scanning node id for workflow %s: %w", workflowID, err)
		}
		validNodeIDs[id] = true
	}
	if err := nodeRows.Err(); err != nil {
		nodeRows.Close()
		return fmt.Errorf("reading node ids for workflow %s: %w", workflowID, err)
	}
	nodeRows.Close()
	for _, c := range conns {
		if !validNodeIDs[c.SourceNodeID] {
			return fmt.Errorf("%w: connection %q references unknown source node %q", ErrDanglingConnection, c.ID, c.SourceNodeID)
		}
		if !validNodeIDs[c.TargetNodeID] {
			return fmt.Errorf("%w: connection %q references unknown target node %q", ErrDanglingConnection, c.ID, c.TargetNodeID)
		}
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO workflow_connections
			(id, workflow_id, source_node_id, source_handle, target_node_id, target_handle, position)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("preparing connection insert: %w", err)
	}
	defer stmt.Close()

	for i := range conns {
		c := &conns[i]
		if c.ID == "" {
			c.ID = newID()
		}
		c.WorkflowID = workflowID
		NormalizeConnectionHandles(conns[i : i+1])

		if _, err := stmt.ExecContext(ctx,
			c.ID, c.WorkflowID, c.SourceNodeID, c.SourceHandle,
			c.TargetNodeID, c.TargetHandle, c.Position,
		); err != nil {
			return fmt.Errorf("inserting connection %s: %w", c.ID, err)
		}
	}
	return nil
}

// loadWorkflowNodes is an internal helper to fetch nodes for a workflow.
func (s *SQLiteWorkflowStore) loadWorkflowNodes(ctx context.Context, workflowID string) ([]WorkflowNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_id, node_type, name, config, position_x, position_y, disabled, created_at, updated_at
		FROM workflow_nodes WHERE workflow_id = ? ORDER BY `+sqltime.SortKey("created_at")+`, rowid`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("loading nodes for workflow %s: %w", workflowID, err)
	}
	defer rows.Close()

	var nodes []WorkflowNode
	for rows.Next() {
		n := WorkflowNode{}
		var disabled int
		var createdAt, updatedAt sqliteTime
		if err := rows.Scan(
			&n.ID, &n.WorkflowID, &n.Type, &n.Name, &n.ConfigRaw,
			&n.PositionX, &n.PositionY, &disabled, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning node row: %w", err)
		}
		n.CreatedAt = createdAt.Time
		n.UpdatedAt = updatedAt.Time
		n.Disabled = disabled != 0
		if err := n.ParseConfig(); err != nil {
			return nil, fmt.Errorf("parsing config for node %s: %w", n.ID, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// loadWorkflowConnections is an internal helper to fetch connections for a workflow.
func (s *SQLiteWorkflowStore) loadWorkflowConnections(ctx context.Context, workflowID string) ([]WorkflowConnection, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, workflow_id, source_node_id, source_handle, target_node_id, target_handle, position
		FROM workflow_connections WHERE workflow_id = ? ORDER BY position ASC`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("loading connections for workflow %s: %w", workflowID, err)
	}
	defer rows.Close()

	var conns []WorkflowConnection
	for rows.Next() {
		c := WorkflowConnection{}
		if err := rows.Scan(
			&c.ID, &c.WorkflowID, &c.SourceNodeID, &c.SourceHandle,
			&c.TargetNodeID, &c.TargetHandle, &c.Position,
		); err != nil {
			return nil, fmt.Errorf("scanning connection row: %w", err)
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}
