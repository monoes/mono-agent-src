package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ---------------------------------------------------------------------------
// Workflows
// ---------------------------------------------------------------------------

type workflowSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsActive    bool   `json:"is_active"`
	Version     int    `json:"version"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

func (mt *MonoagentTools) listWorkflows(string) (string, error) {
	rows, err := mt.db.Query(
		`SELECT id, name, COALESCE(description,''), is_active, version, created_at, updated_at
		 FROM workflows WHERE COALESCE(profile_id,'default') = ? ORDER BY updated_at DESC`,
		mt.ProfileID())
	if err != nil {
		return "", fmt.Errorf("query workflows: %w", err)
	}
	defer rows.Close()

	out := make([]workflowSummary, 0)
	for rows.Next() {
		var w workflowSummary
		if err := rows.Scan(&w.ID, &w.Name, &w.Description, &w.IsActive, &w.Version, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return "", fmt.Errorf("scan workflow: %w", err)
		}
		out = append(out, w)
	}
	return marshalJSON(map[string]interface{}{"workflows": out})
}

type workflowIDArgs struct {
	WorkflowID string `json:"workflow_id"`
}

func (mt *MonoagentTools) getWorkflow(args string) (string, error) {
	var a workflowIDArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}

	var w workflowSummary
	if err := mt.db.QueryRow(
		`SELECT id, name, COALESCE(description,''), is_active, version, created_at, updated_at
		 FROM workflows WHERE id = ?`, a.WorkflowID,
	).Scan(&w.ID, &w.Name, &w.Description, &w.IsActive, &w.Version, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return "", fmt.Errorf("query workflow: %w", err)
	}

	rows, err := mt.db.Query(
		`SELECT id, node_type, name, config, position_x, position_y, disabled
		 FROM workflow_nodes WHERE workflow_id = ?`, a.WorkflowID)
	if err != nil {
		return "", fmt.Errorf("query nodes: %w", err)
	}
	defer rows.Close()
	nodes := make([]nodeRow, 0)
	for rows.Next() {
		var n nodeRow
		var configStr string
		if err := rows.Scan(&n.ID, &n.NodeType, &n.Name, &configStr, &n.PositionX, &n.PositionY, &n.Disabled); err != nil {
			return "", fmt.Errorf("scan node: %w", err)
		}
		var cfg interface{}
		if configStr != "" {
			if err := json.Unmarshal([]byte(configStr), &cfg); err == nil {
				// Same redaction the canvas tools apply (tools.go) — node
				// configs can carry pasted api_key/token values, and this
				// result crosses the LLM boundary.
				n.Config = redactConfigSecrets(cfg)
			} else {
				n.Config = configStr
			}
		} else {
			n.Config = map[string]interface{}{}
		}
		nodes = append(nodes, n)
	}

	connRows, err := mt.db.Query(
		`SELECT id, source_node_id, source_handle, target_node_id, target_handle, position
		 FROM workflow_connections WHERE workflow_id = ?`, a.WorkflowID)
	if err != nil {
		return "", fmt.Errorf("query connections: %w", err)
	}
	defer connRows.Close()
	connections := make([]connectionRow, 0)
	for connRows.Next() {
		var c connectionRow
		if err := connRows.Scan(&c.ID, &c.SourceNodeID, &c.SourceHandle, &c.TargetNodeID, &c.TargetHandle, &c.Position); err != nil {
			return "", fmt.Errorf("scan connection: %w", err)
		}
		connections = append(connections, c)
	}

	return marshalJSON(map[string]interface{}{
		"workflow":    w,
		"nodes":       nodes,
		"connections": connections,
	})
}

func (mt *MonoagentTools) deleteWorkflow(args string) (string, error) {
	var a workflowIDArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}

	// Snapshot everything user-authored that the delete removes (workflow
	// row, nodes, connections — run-history executions are logs, not
	// rebuildable content) and fail closed if the backup can't be written.
	tables := map[string][]map[string]interface{}{}
	var err error
	if tables["workflows"], err = snapshotRows(mt.db,
		`SELECT * FROM workflows WHERE id = ? AND COALESCE(profile_id,'default') = ?`,
		a.WorkflowID, mt.ProfileID()); err != nil {
		return "", fmt.Errorf("snapshot workflow: %w", err)
	}
	if len(tables["workflows"]) == 0 {
		return "", fmt.Errorf("workflow %s not found", a.WorkflowID)
	}
	if tables["workflow_nodes"], err = snapshotRows(mt.db,
		`SELECT * FROM workflow_nodes WHERE workflow_id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("snapshot nodes: %w", err)
	}
	if tables["workflow_connections"], err = snapshotRows(mt.db,
		`SELECT * FROM workflow_connections WHERE workflow_id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("snapshot connections: %w", err)
	}
	backupPath, err := saveMonoToolBackup("workflow", a.WorkflowID, "delete", tables)
	if err != nil {
		return "", fmt.Errorf("snapshot before delete: %w", err)
	}

	tx, err := mt.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM workflow_connections WHERE workflow_id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("delete connections: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM workflow_nodes WHERE workflow_id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("delete nodes: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM workflow_executions WHERE workflow_id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("delete executions: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM workflows WHERE id = ?`, a.WorkflowID); err != nil {
		return "", fmt.Errorf("delete workflow: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	// Remove the workflow file too: otherwise the store still lists the
	// workflow (its file wins) after its rows are gone.
	if st := mt.workflowStore(); st != nil {
		if err := st.DeleteWorkflow(context.Background(), a.WorkflowID); err != nil {
			return "", fmt.Errorf("delete workflow file: %w", err)
		}
	}
	return marshalJSON(map[string]interface{}{"deleted_workflow_id": a.WorkflowID, "backup_path": backupPath})
}

type setWorkflowActiveArgs struct {
	WorkflowID string `json:"workflow_id"`
	Active     bool   `json:"active"`
}

func (mt *MonoagentTools) setWorkflowActive(args string) (string, error) {
	var a setWorkflowActiveArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}
	if st := mt.workflowStore(); st != nil {
		// The workflow file is the definition the engine reads; the
		// store updates it and its SQLite mirror together.
		if err := st.SetWorkflowActive(context.Background(), a.WorkflowID, a.Active); err != nil {
			return "", fmt.Errorf("update workflow: %w", err)
		}
	} else if _, err := mt.db.Exec(
		`UPDATE workflows SET is_active = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		a.Active, a.WorkflowID); err != nil {
		return "", fmt.Errorf("update workflow: %w", err)
	}
	return marshalJSON(map[string]interface{}{"workflow_id": a.WorkflowID, "is_active": a.Active})
}

type runWorkflowArgs struct {
	WorkflowID string `json:"workflow_id"`
	Confirm    bool   `json:"confirm"`
}

// runSelfExec is the exec boundary for run_workflow — a package
// var so tests can stub subprocess execution without a real binary.
var runSelfExec = func(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	return cmd.CombinedOutput()
}

// maxRunErrOutputBytes caps embedded subprocess output inside error
// strings so a chatty child can't balloon the error (and the model
// context it lands in).
const maxRunErrOutputBytes = 4 * 1024

// truncateRunErrOutput bounds embedded CombinedOutput text in errors.
func truncateRunErrOutput(s string) string {
	if len(s) <= maxRunErrOutputBytes {
		return s
	}
	return s[:maxRunErrOutputBytes] + "...[truncated]"
}

// maxRunExecTimeout caps run-tool subprocess lifetime. The effective
// timeout is min(maxRunExecTimeout, remaining ctx deadline) so the child
// never outlives the OnToolCall context it derives from.
const maxRunExecTimeout = 2 * time.Minute

// runExecTimeoutCtx derives the child's context: from the caller's ctx
// (never Background), bounded by maxRunExecTimeout. An already-expired or
// cancelled caller ctx refuses before anything is spawned.
func runExecTimeoutCtx(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	timeout := maxRunExecTimeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			if remaining <= 0 {
				return nil, nil, ctx.Err()
			}
			timeout = remaining
		}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	return cctx, cancel, nil
}

func (mt *MonoagentTools) runWorkflow(ctx context.Context, args string) (string, error) {
	var a runWorkflowArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkRunGate("run_workflow"); err != nil {
		return "", err
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}
	if !a.Confirm {
		return marshalJSON(map[string]interface{}{
			"would_run":   true,
			"workflow_id": a.WorkflowID,
			"note":        "this would trigger a real workflow run; call again with confirm:true to actually execute it",
		})
	}
	if mt.selfBin == "" {
		return "", fmt.Errorf("run_workflow: execution is unavailable in this session")
	}
	cctx, cancel, err := runExecTimeoutCtx(ctx)
	if err != nil {
		return "", fmt.Errorf("run workflow: %w", err)
	}
	defer cancel()
	out, err := runSelfExec(cctx, mt.selfBin, "workflow", "run", a.WorkflowID, "--profile", mt.ProfileID())
	if err != nil {
		return "", fmt.Errorf("run workflow: %w: %s", err, truncateRunErrOutput(string(out)))
	}
	return marshalJSON(map[string]interface{}{"workflow_id": a.WorkflowID, "ran": true, "output": string(out)})
}

type mtCreateWorkflowArgs struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (mt *MonoagentTools) createWorkflow(args string) (string, error) {
	var a mtCreateWorkflowArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return "", fmt.Errorf("name is required")
	}
	id := "wf-" + uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	// created_at/updated_at passed explicitly — their DB-level defaults are
	// broken on existing installs (verified live; see the identical note in
	// internal/storage/actions_migration.go), so every INSERT into
	// workflows/workflow_nodes must set them itself.
	if _, err := mt.db.Exec(
		`INSERT INTO workflows (id, name, description, is_active, version, profile_id, created_at, updated_at)
		 VALUES (?, ?, ?, 0, 1, ?, ?, ?)`,
		id, a.Name, a.Description, mt.ProfileID(), now, now); err != nil {
		return "", fmt.Errorf("insert workflow: %w", err)
	}
	return marshalJSON(map[string]interface{}{"workflow_id": id})
}

type mtListNodeTypesArgs struct {
	Category string `json:"category"`
}

// listNodeTypes surfaces the same node-type registry the workflow engine
// itself validates node_type against (internal/noderegistry.Build) — the
// single source of truth for what add_workflow_node will actually accept.
func (mt *MonoagentTools) listNodeTypes(args string) (string, error) {
	var a mtListNodeTypesArgs
	if args != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "", fmt.Errorf("invalid args: %w", err)
		}
	}
	reg := noderegistry.Build(mt.db)
	types := append(reg.Types(), triggerNodeTypes()...)
	sort.Strings(types)
	out := make([]string, 0, len(types))
	for _, t := range types {
		if a.Category != "" && !strings.HasPrefix(t, a.Category+".") {
			continue
		}
		out = append(out, t)
	}
	return marshalJSON(map[string]interface{}{"node_types": out})
}

type mtAddWorkflowNodeArgs struct {
	WorkflowID string                 `json:"workflow_id"`
	NodeType   string                 `json:"node_type"`
	Name       string                 `json:"name"`
	Config     map[string]interface{} `json:"config"`
}

func (mt *MonoagentTools) addWorkflowNode(args string) (string, error) {
	var a mtAddWorkflowNodeArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if err := mt.checkWorkflowOwnership(a.WorkflowID); err != nil {
		return "", err
	}
	if !noderegistry.Build(mt.db).Has(a.NodeType) && !isTriggerNodeType(a.NodeType) {
		return "", fmt.Errorf("unknown node_type %q — call list_node_types (MCP: node_list) to see what's available", a.NodeType)
	}
	if a.Config == nil {
		a.Config = map[string]interface{}{}
	}
	configJSON, err := json.Marshal(a.Config)
	if err != nil {
		return "", fmt.Errorf("encoding config: %w", err)
	}
	id := "node-" + uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := mt.db.Exec(
		`INSERT INTO workflow_nodes (id, workflow_id, node_type, name, config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, a.WorkflowID, a.NodeType, a.Name, string(configJSON), now, now); err != nil {
		return "", fmt.Errorf("insert node: %w", err)
	}
	return marshalJSON(map[string]interface{}{"node_id": id, "workflow_id": a.WorkflowID, "node_type": a.NodeType})
}

type templateSummary struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Subject string `json:"subject,omitempty"`
}

func (mt *MonoagentTools) listTemplates(string) (string, error) {
	// Templates are not profile-scoped (shared across the install).
	rows, err := mt.db.Query(`SELECT id, name, COALESCE(subject,'') FROM templates ORDER BY name`)
	if err != nil {
		return "", fmt.Errorf("query templates: %w", err)
	}
	defer rows.Close()
	out := make([]templateSummary, 0)
	for rows.Next() {
		var t templateSummary
		if err := rows.Scan(&t.ID, &t.Name, &t.Subject); err != nil {
			return "", fmt.Errorf("scan template: %w", err)
		}
		out = append(out, t)
	}
	return marshalJSON(map[string]interface{}{"templates": out})
}

// triggerNodeTypes lists the trigger types: they have embedded schemas but
// run through the trigger manager, so the node registry does not hold them.
func triggerNodeTypes() []string {
	var out []string
	for _, t := range workflow.ListEmbeddedSchemas() {
		if strings.HasPrefix(t, "trigger.") {
			out = append(out, t)
		}
	}
	return out
}

func isTriggerNodeType(t string) bool {
	return slices.Contains(triggerNodeTypes(), t)
}
