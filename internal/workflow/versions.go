package workflow

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// WorkflowSnapshot is the part of a workflow a version records: what the
// workflow does (settings included), not whether it is active or when it
// was touched. Node schemas are left out — they are derived from the node
// type.
type WorkflowSnapshot struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Settings    map[string]interface{} `json:"settings,omitempty"`
	Nodes       []SnapshotNode         `json:"nodes"`
	Connections []SnapshotConnection   `json:"connections"`
}

// SnapshotNode is one node of a WorkflowSnapshot.
type SnapshotNode struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"node_type"`
	Name      string                 `json:"name"`
	Config    map[string]interface{} `json:"config"`
	PositionX float64                `json:"position_x"`
	PositionY float64                `json:"position_y"`
	Disabled  bool                   `json:"disabled"`
}

// SnapshotConnection is one connection of a WorkflowSnapshot.
type SnapshotConnection struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	SourceHandle string `json:"source_handle"`
	TargetNodeID string `json:"target_node_id"`
	TargetHandle string `json:"target_handle"`
	Position     int    `json:"position"`
}

// WorkflowVersion is one recorded definition of a workflow.
type WorkflowVersion struct {
	WorkflowID string            `json:"workflow_id"`
	Version    int               `json:"version"`
	ProfileID  string            `json:"profile_id"`
	Note       string            `json:"note"`
	Hash       string            `json:"definition_hash"`
	CreatedAt  time.Time         `json:"created_at"`
	Snapshot   *WorkflowSnapshot `json:"snapshot,omitempty"`
}

// snapshotOf captures w's definition.
func snapshotOf(w *Workflow) *WorkflowSnapshot {
	s := &WorkflowSnapshot{
		Name:        w.Name,
		Description: w.Description,
		Settings:    w.Settings,
		Nodes:       make([]SnapshotNode, 0, len(w.Nodes)),
		Connections: make([]SnapshotConnection, 0, len(w.Connections)),
	}
	for _, n := range w.Nodes {
		cfg := n.Config
		if cfg == nil && n.ConfigRaw != "" {
			_ = json.Unmarshal([]byte(n.ConfigRaw), &cfg)
		}
		if cfg == nil {
			cfg = map[string]interface{}{}
		}
		s.Nodes = append(s.Nodes, SnapshotNode{
			ID: n.ID, Type: n.Type, Name: n.Name, Config: cfg,
			PositionX: n.PositionX, PositionY: n.PositionY, Disabled: n.Disabled,
		})
	}
	for _, c := range w.Connections {
		s.Connections = append(s.Connections, SnapshotConnection{
			ID: c.ID, SourceNodeID: c.SourceNodeID, SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID, TargetHandle: c.TargetHandle, Position: c.Position,
		})
	}
	return s
}

// hash returns the hash of s's canonical form: nodes and connections sorted,
// connection positions dropped (the file store does not keep them), so the
// same definition read back from either store hashes the same.
func (s *WorkflowSnapshot) hash() (string, error) {
	c := WorkflowSnapshot{Name: s.Name, Description: s.Description, Settings: s.Settings}
	c.Nodes = append([]SnapshotNode(nil), s.Nodes...)
	c.Connections = append([]SnapshotConnection(nil), s.Connections...)
	sort.SliceStable(c.Nodes, func(i, j int) bool { return c.Nodes[i].ID < c.Nodes[j].ID })
	for i := range c.Connections {
		c.Connections[i].Position = 0
	}
	sort.SliceStable(c.Connections, func(i, j int) bool {
		a, b := c.Connections[i], c.Connections[j]
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.SourceNodeID != b.SourceNodeID {
			return a.SourceNodeID < b.SourceNodeID
		}
		if a.TargetNodeID != b.TargetNodeID {
			return a.TargetNodeID < b.TargetNodeID
		}
		return a.SourceHandle+"\x00"+a.TargetHandle < b.SourceHandle+"\x00"+b.TargetHandle
	})
	data, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("hashing workflow snapshot: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// definitionHash is the snapshot hash of w's current definition.
func definitionHash(w *Workflow) (string, error) {
	return snapshotOf(w).hash()
}

// Apply returns a copy of base whose definition (name, description,
// settings, nodes, connections) is s. Identity, profile and activation stay
// base's.
func (s *WorkflowSnapshot) Apply(base *Workflow) *Workflow {
	out := *base
	out.Name, out.Description, out.Settings = s.Name, s.Description, s.Settings
	out.Nodes = make([]WorkflowNode, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		wn := WorkflowNode{
			ID: n.ID, WorkflowID: base.ID, Type: n.Type, Name: n.Name, Config: n.Config,
			PositionX: n.PositionX, PositionY: n.PositionY, Disabled: n.Disabled,
		}
		if wn.Config == nil {
			wn.Config = map[string]interface{}{}
		}
		_ = wn.MarshalConfig()
		out.Nodes = append(out.Nodes, wn)
	}
	out.Connections = make([]WorkflowConnection, 0, len(s.Connections))
	for _, c := range s.Connections {
		out.Connections = append(out.Connections, WorkflowConnection{
			ID: c.ID, WorkflowID: base.ID, SourceNodeID: c.SourceNodeID, SourceHandle: c.SourceHandle,
			TargetNodeID: c.TargetNodeID, TargetHandle: c.TargetHandle, Position: c.Position,
		})
	}
	return &out
}

// recordVersionTx records w's definition as a new version unless it equals
// the latest recorded one, sets the workflow row's version to the result and
// returns it. tx must already hold the write lock (have written), so the
// version read here cannot go stale before the insert.
//
// storedVersion is the version the workflow carried before this write (0 for
// a new workflow). A save of a changed definition gets the next number after
// it and the latest snapshot; capture (recording what is already stored, for
// a workflow saved before versions existed or edited outside a save) keeps
// storedVersion for the first snapshot rather than inventing a change.
func recordVersionTx(ctx context.Context, tx *sql.Tx, w *Workflow, storedVersion int, capture bool, note string) (int, bool, error) {
	snap := snapshotOf(w)
	hash, err := snap.hash()
	if err != nil {
		return 0, false, err
	}
	var latest int
	var latestHash string
	err = tx.QueryRowContext(ctx, `
		SELECT version, definition_hash FROM workflow_versions
		WHERE workflow_id = ? ORDER BY version DESC LIMIT 1`, w.ID).Scan(&latest, &latestHash)
	if err != nil && err != sql.ErrNoRows {
		return 0, false, fmt.Errorf("reading latest version of workflow %s: %w", w.ID, err)
	}

	v, created := latest, false
	if latest == 0 || latestHash != hash {
		v, created = max(latest, storedVersion)+1, true
		if capture && latest == 0 && storedVersion > 0 {
			v = storedVersion
		}
		data, err := json.Marshal(snap)
		if err != nil {
			return 0, false, fmt.Errorf("encoding snapshot of workflow %s: %w", w.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO workflow_versions (workflow_id, version, snapshot, definition_hash, profile_id, note, created_at)
			VALUES (?, ?, ?, ?, (SELECT profile_id FROM workflows WHERE id = ?), ?, ?)`,
			w.ID, v, string(data), hash, w.ID, note, time.Now().UTC()); err != nil {
			return 0, false, fmt.Errorf("recording version %d of workflow %s: %w", v, w.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workflows SET version = ? WHERE id = ?`, v, w.ID); err != nil {
		return 0, false, fmt.Errorf("setting version of workflow %s: %w", w.ID, err)
	}
	return v, created, nil
}

// captureVersion records w's stored definition as a version if it is not the
// latest one already, and returns the version it has. Zero with no error
// means the workflow has no SQLite row to hang a version on.
func (s *SQLiteWorkflowStore) captureVersion(ctx context.Context, w *Workflow) (int, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("beginning capture-version transaction: %w", err)
	}
	defer tx.Rollback()
	// A no-op write first, to take the write lock before reading the latest
	// version (see recordVersionTx).
	res, err := tx.ExecContext(ctx, `UPDATE workflows SET version = version WHERE id = ?`, w.ID)
	if err != nil {
		return 0, false, fmt.Errorf("locking workflow %s: %w", w.ID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, false, nil
	}
	var rowVersion int
	if err := tx.QueryRowContext(ctx, `SELECT version FROM workflows WHERE id = ?`, w.ID).Scan(&rowVersion); err != nil {
		return 0, false, fmt.Errorf("reading version of workflow %s: %w", w.ID, err)
	}
	v, created, err := recordVersionTx(ctx, tx, w, max(rowVersion, w.Version), true, "")
	if err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("committing capture-version transaction: %w", err)
	}
	return v, created, nil
}

// ListWorkflowVersions returns a workflow's recorded versions, newest first,
// snapshots included.
func (s *SQLiteWorkflowStore) ListWorkflowVersions(ctx context.Context, workflowID string) ([]WorkflowVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT workflow_id, version, profile_id, note, definition_hash, created_at, snapshot
		FROM workflow_versions WHERE workflow_id = ? ORDER BY version DESC`, workflowID)
	if err != nil {
		return nil, fmt.Errorf("listing versions of workflow %s: %w", workflowID, err)
	}
	defer rows.Close()
	out := []WorkflowVersion{}
	for rows.Next() {
		v, err := scanWorkflowVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

// GetWorkflowVersion returns one recorded version, or nil, nil when there is
// no such version.
func (s *SQLiteWorkflowStore) GetWorkflowVersion(ctx context.Context, workflowID string, version int) (*WorkflowVersion, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT workflow_id, version, profile_id, note, definition_hash, created_at, snapshot
		FROM workflow_versions WHERE workflow_id = ? AND version = ?`, workflowID, version)
	v, err := scanWorkflowVersion(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func scanWorkflowVersion(row interface{ Scan(...any) error }) (*WorkflowVersion, error) {
	var v WorkflowVersion
	var createdAt sqliteTime
	var raw string
	if err := row.Scan(&v.WorkflowID, &v.Version, &v.ProfileID, &v.Note, &v.Hash, &createdAt, &raw); err != nil {
		if err == sql.ErrNoRows {
			return nil, err
		}
		return nil, fmt.Errorf("scanning workflow version: %w", err)
	}
	v.CreatedAt = createdAt.Time
	v.Snapshot = &WorkflowSnapshot{}
	if err := json.Unmarshal([]byte(raw), v.Snapshot); err != nil {
		return nil, fmt.Errorf("parsing snapshot of workflow %s version %d: %w", v.WorkflowID, v.Version, err)
	}
	return &v, nil
}

// PruneWorkflowVersions deletes a workflow's versions except the keep newest
// and any version an unfinished (queued, running or waiting) execution is
// pinned to. It returns how many versions were deleted.
func (s *SQLiteWorkflowStore) PruneWorkflowVersions(ctx context.Context, workflowID string, keep int) (int64, error) {
	if keep < 1 {
		return 0, fmt.Errorf("prune versions: keep must be at least 1, got %d", keep)
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM workflow_versions
		WHERE workflow_id = ?
		  AND version NOT IN (
			SELECT version FROM workflow_versions WHERE workflow_id = ? ORDER BY version DESC LIMIT ?)
		  AND version NOT IN (
			SELECT workflow_version FROM workflow_executions
			WHERE workflow_id = ? AND workflow_version IS NOT NULL
			  AND status IN ('QUEUED', 'RUNNING', 'WAITING'))`,
		workflowID, workflowID, keep, workflowID)
	if err != nil {
		return 0, fmt.Errorf("pruning versions of workflow %s: %w", workflowID, err)
	}
	return res.RowsAffected()
}

// ExecutionWorkflowVersion returns the workflow version an execution was
// created against; zero when it was not pinned.
func (s *SQLiteWorkflowStore) ExecutionWorkflowVersion(ctx context.Context, executionID string) (int, error) {
	var v sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT workflow_version FROM workflow_executions WHERE id = ?`, executionID).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading pinned version of execution %s: %w", executionID, err)
	}
	return int(v.Int64), nil
}

// pinExecutionVersion records the workflow version an execution runs.
func (s *SQLiteWorkflowStore) pinExecutionVersion(ctx context.Context, executionID string, version int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE workflow_executions SET workflow_version = ? WHERE id = ?`, version, executionID)
	if err != nil {
		return fmt.Errorf("pinning execution %s to version %d: %w", executionID, version, err)
	}
	return nil
}
