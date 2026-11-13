package chat

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/dbconn"
)

type deleteNodesArgs struct {
	WorkflowID string   `json:"workflow_id"`
	NodeIDs    []string `json:"node_ids"`
}

// aiBackupDir resolves the directory holding AI-delete snapshots
// (~/.monoagent/workflows, next to the exported workflow JSON files).
// Package var so tests can redirect it.
var aiBackupDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".monoagent", "workflows"), nil
}

// maxAIBackupSnapshots bounds the per-workflow rotation of AI-delete
// snapshots kept in <id>.ai-backup.json.
const maxAIBackupSnapshots = 3

// deleteBackup is the on-disk shape of <workflow>.ai-backup.json: the most
// recent maxAIBackupSnapshots delete operations, newest first.
type deleteBackup struct {
	WorkflowID string           `json:"workflow_id"`
	Snapshots  []deleteSnapshot `json:"snapshots"`
}

type deleteSnapshot struct {
	CreatedAt   string          `json:"created_at"`
	Nodes       []nodeRow       `json:"nodes"`
	Connections []connectionRow `json:"connections"`
}

// saveDeleteBackup persists snap as the newest entry in the workflow's
// ai-backup sidecar (rotation keeps the last maxAIBackupSnapshots). The
// removed nodes/connections are recoverable from it by hand, so a wrong
// delete_nodes call is no longer irreversible.
func saveDeleteBackup(workflowID string, snap deleteSnapshot) (string, error) {
	dir, err := aiBackupDir()
	if err != nil {
		return "", fmt.Errorf("resolve backup dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	path := filepath.Join(dir, workflowID+".ai-backup.json")

	var backup deleteBackup
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &backup) // corrupt sidecar: start a fresh rotation
	}
	snap.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	backup.WorkflowID = workflowID
	backup.Snapshots = append([]deleteSnapshot{snap}, backup.Snapshots...)
	if len(backup.Snapshots) > maxAIBackupSnapshots {
		backup.Snapshots = backup.Snapshots[:maxAIBackupSnapshots]
	}
	b, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal backup: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}
	return path, nil
}

func (ct *CanvasTools) deleteNodes(args string) (string, error) {
	var a deleteNodesArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := rejectPlaceholderWorkflowID(a.WorkflowID); err != nil {
		return "", err
	}
	if err := ct.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}

	// The write lock is taken first (dbconn.WithWriteTx): the nodes are read before they are deleted, and a writer
	// committing in between would fail the delete with SQLITE_BUSY_SNAPSHOT (#1460). The sidecar write stays
	// inside the transaction, as it was: a backup failure rolls the delete back.
	var snap deleteSnapshot
	var backupPath string
	began, bodyDone := false, false
	err := dbconn.WithWriteTx(context.Background(), ct.db, func(conn *sql.Conn) error {
		began = true
		var err error
		if snap, err = snapshotAndDeleteNodes(dbconn.ConnTx{Ctx: context.Background(), Conn: conn}, a); err != nil {
			return err
		}
		if backupPath, err = saveDeleteBackup(a.WorkflowID, snap); err != nil {
			return fmt.Errorf("snapshot before delete: %w", err)
		}
		bodyDone = true
		return nil
	})
	if err != nil {
		switch {
		case !began:
			return "", fmt.Errorf("begin tx: %w", err)
		case bodyDone:
			return "", fmt.Errorf("commit: %w", err)
		}
		return "", err
	}

	result := map[string]interface{}{
		"deleted_node_ids":    a.NodeIDs,
		"deleted_nodes":       snap.Nodes,
		"deleted_connections": snap.Connections,
		"backup_path":         backupPath,
	}
	return marshalJSON(result)
}

// snapshotAndDeleteNodes snapshots the nodes named in a, with their connections, and deletes them, inside tx.
func snapshotAndDeleteNodes(tx dbconn.ConnTx, a deleteNodesArgs) (deleteSnapshot, error) {
	// Snapshot the affected nodes + connections BEFORE deleting so the
	// operation is reversible; a backup failure aborts the delete (fail
	// closed — an irreversible op must not proceed unsnapshotted).
	snap := deleteSnapshot{Nodes: []nodeRow{}, Connections: []connectionRow{}}
	for _, nodeID := range a.NodeIDs {
		var n nodeRow
		var configStr string
		err := tx.QueryRow(
			`SELECT id, node_type, name, config, position_x, position_y, disabled
			 FROM workflow_nodes WHERE id = ? AND workflow_id = ?`,
			nodeID, a.WorkflowID).Scan(&n.ID, &n.NodeType, &n.Name, &configStr, &n.PositionX, &n.PositionY, &n.Disabled)
		if err == sql.ErrNoRows {
			continue // nothing to delete under this id
		}
		if err != nil {
			return deleteSnapshot{}, fmt.Errorf("query node %s: %w", nodeID, err)
		}
		if configStr != "" {
			var cfg interface{}
			if err := json.Unmarshal([]byte(configStr), &cfg); err == nil {
				n.Config = cfg
			} else {
				n.Config = configStr
			}
		} else {
			n.Config = map[string]interface{}{}
		}
		snap.Nodes = append(snap.Nodes, n)

		connRows, err := tx.Query(
			`SELECT id, source_node_id, source_handle, target_node_id, target_handle, position
			 FROM workflow_connections WHERE workflow_id = ? AND (source_node_id = ? OR target_node_id = ?)`,
			a.WorkflowID, nodeID, nodeID)
		if err != nil {
			return deleteSnapshot{}, fmt.Errorf("query connections for node %s: %w", nodeID, err)
		}
		for connRows.Next() {
			var c connectionRow
			if err := connRows.Scan(&c.ID, &c.SourceNodeID, &c.SourceHandle, &c.TargetNodeID, &c.TargetHandle, &c.Position); err != nil {
				connRows.Close()
				return deleteSnapshot{}, fmt.Errorf("scan connection: %w", err)
			}
			snap.Connections = append(snap.Connections, c)
		}
		connRows.Close()
		if err := connRows.Err(); err != nil {
			return deleteSnapshot{}, fmt.Errorf("iterate connections: %w", err)
		}

		// Delete connections involving this node
		if _, err := tx.Exec(
			`DELETE FROM workflow_connections WHERE workflow_id = ? AND (source_node_id = ? OR target_node_id = ?)`,
			a.WorkflowID, nodeID, nodeID); err != nil {
			return deleteSnapshot{}, fmt.Errorf("delete connections for node %s: %w", nodeID, err)
		}
		// Delete the node
		if _, err := tx.Exec(
			`DELETE FROM workflow_nodes WHERE id = ? AND workflow_id = ?`,
			nodeID, a.WorkflowID); err != nil {
			return deleteSnapshot{}, fmt.Errorf("delete node %s: %w", nodeID, err)
		}
	}

	return snap, nil
}

type connectNodesArgs struct {
	WorkflowID   string `json:"workflow_id"`
	SourceNodeID string `json:"source_node_id"`
	SourceHandle string `json:"source_handle"`
	TargetNodeID string `json:"target_node_id"`
	TargetHandle string `json:"target_handle"`
}

func (ct *CanvasTools) connectNodes(args string) (string, error) {
	var a connectNodesArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := rejectPlaceholderWorkflowID(a.WorkflowID); err != nil {
		return "", err
	}
	if err := ct.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}
	// Both endpoints must exist before wiring a connection — otherwise a
	// hallucinated node id creates a dangling edge the canvas can't render.
	for _, nodeID := range []string{a.SourceNodeID, a.TargetNodeID} {
		var exists int
		err := ct.db.QueryRow(
			`SELECT 1 FROM workflow_nodes WHERE id = ? AND workflow_id = ?`,
			nodeID, a.WorkflowID).Scan(&exists)
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("cannot connect: node %s not found in workflow %s", nodeID, a.WorkflowID)
		}
		if err != nil {
			return "", fmt.Errorf("check node %s: %w", nodeID, err)
		}
	}

	id := uuid.New().String()
	if _, err := ct.db.Exec(
		`INSERT INTO workflow_connections (id, workflow_id, source_node_id, source_handle, target_node_id, target_handle, position)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		id, a.WorkflowID, a.SourceNodeID, a.SourceHandle, a.TargetNodeID, a.TargetHandle); err != nil {
		return "", fmt.Errorf("insert connection: %w", err)
	}

	result := map[string]interface{}{
		"connection_id": id,
	}
	return marshalJSON(result)
}

type disconnectNodesArgs struct {
	WorkflowID   string `json:"workflow_id"`
	SourceNodeID string `json:"source_node_id"`
	TargetNodeID string `json:"target_node_id"`
}

func (ct *CanvasTools) disconnectNodes(args string) (string, error) {
	var a disconnectNodesArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := rejectPlaceholderWorkflowID(a.WorkflowID); err != nil {
		return "", err
	}
	if err := ct.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}

	res, err := ct.db.Exec(
		`DELETE FROM workflow_connections WHERE workflow_id = ? AND source_node_id = ? AND target_node_id = ?`,
		a.WorkflowID, a.SourceNodeID, a.TargetNodeID)
	if err != nil {
		return "", fmt.Errorf("delete connection: %w", err)
	}

	deleted, _ := res.RowsAffected()
	result := map[string]interface{}{
		"deleted_count": deleted,
	}
	return marshalJSON(result)
}

type listAvailableNodesArgs struct {
	Category string `json:"category"`
}

func (ct *CanvasTools) listAvailableNodes(args string) (string, error) {
	var a listAvailableNodesArgs
	if args != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}

	if a.Category == "" {
		return marshalJSON(map[string]interface{}{"node_types": ct.nodeTypes})
	}

	filtered := make([]NodeTypeInfo, 0)
	for _, nt := range ct.nodeTypes {
		if nt.Category == a.Category {
			filtered = append(filtered, nt)
		}
	}
	return marshalJSON(map[string]interface{}{"node_types": filtered})
}
