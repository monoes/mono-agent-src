package workflowedit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
	"github.com/spf13/cobra"
)

// ── node subcommand group ────────────────────────────────────────────────────

// newWorkflowNodeCmd is the `workflow node` parent command.
func (d Deps) newNodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Manage nodes within a workflow",
	}
	cmd.AddCommand(
		d.newNodeAddCmd(),
		d.newNodeListCmd(),
		d.newNodeSetCmd(),
		d.newNodeRemoveCmd(),
	)
	return cmd
}

// newWorkflowNodeAddCmd adds a node to a workflow.
func (d Deps) newNodeAddCmd() *cobra.Command {
	var nodeType, name, configJSON string
	var posX, posY float64

	cmd := &cobra.Command{
		Use:   "add <workflow-id>",
		Short: "Add a node to a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			// Ensure workflow exists.
			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), workflowID)
			expect := reviewOf(wf) // what the operator is looking at, before any edit
			if wf == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}

			// Fetch existing nodes so we can append.
			existing := wf.Nodes

			// Parse --config JSON.
			var config map[string]interface{}
			if configJSON != "" {
				if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
					return fmt.Errorf("parse --config JSON: %w", err)
				}
			} else {
				config = make(map[string]interface{})
			}

			newNode := workflow.WorkflowNode{
				ID:         uuid.New().String(),
				WorkflowID: workflowID,
				Type:       nodeType,
				Name:       name,
				Config:     config,
				PositionX:  posX,
				PositionY:  posY,
			}
			if err := newNode.MarshalConfig(); err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}

			existing = append(existing, newNode)
			if err := store.SaveWorkflowNodes(approving(ctx, expect), workflowID, existing); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(newNode)
			}
			fmt.Fprintf(os.Stdout, "Added node %s (type: %s, id: %s)\n", newNode.Name, newNode.Type, newNode.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&nodeType, "type", "", "Node type (e.g. core.if, trigger.schedule) [required]")
	cmd.Flags().StringVar(&name, "name", "", "Display name for the node [required]")
	cmd.Flags().StringVar(&configJSON, "config", "", "Node configuration as JSON object")
	cmd.Flags().Float64Var(&posX, "x", 0, "Canvas X position")
	cmd.Flags().Float64Var(&posY, "y", 0, "Canvas Y position")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("name")
	return cmd
}

// newWorkflowNodeListCmd lists the nodes in a workflow.
func (d Deps) newNodeListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list <workflow-id>",
		Short: "List nodes in a workflow",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), args[0])
			if wf == nil {
				return d.NotFound("workflow %q not found", args[0])
			}

			if jsonOut || d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(wf.Nodes)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tNAME\tX\tY\tCONFIG")
			for _, n := range wf.Nodes {
				configStr := n.ConfigRaw
				if len(configStr) > 60 {
					configStr = configStr[:57] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%.0f\t%.0f\t%s\n",
					n.ID, n.Type, n.Name, n.PositionX, n.PositionY, configStr)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	return cmd
}

// newWorkflowNodeSetCmd updates a node's configuration and/or position.
func (d Deps) newNodeSetCmd() *cobra.Command {
	var configJSON, name string
	var posX, posY float64
	var setPosX, setPosY bool

	cmd := &cobra.Command{
		Use:   "set <workflow-id> <node-id>",
		Short: "Update a node's config, name, or position",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, nodeID := args[0], args[1]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), workflowID)
			expect := reviewOf(wf) // what the operator is looking at, before any edit
			if wf == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}

			found := false
			for i := range wf.Nodes {
				if wf.Nodes[i].ID != nodeID {
					continue
				}
				found = true
				if name != "" {
					wf.Nodes[i].Name = name
				}
				if configJSON != "" {
					var config map[string]interface{}
					if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
						return d.InvalidInput("parse --config JSON: %v", err)
					}
					wf.Nodes[i].Config = config
					if err := wf.Nodes[i].MarshalConfig(); err != nil {
						return fmt.Errorf("marshal config: %w", err)
					}
				}
				if setPosX {
					wf.Nodes[i].PositionX = posX
				}
				if setPosY {
					wf.Nodes[i].PositionY = posY
				}
				break
			}
			if !found {
				return d.NotFound("node %q not found in workflow %q", nodeID, workflowID)
			}

			if err := store.SaveWorkflowNodes(approving(ctx, expect), workflowID, wf.Nodes); err != nil {
				return fmt.Errorf("save nodes: %w", err)
			}
			if d.JSONOutput() {
				for i := range wf.Nodes {
					if wf.Nodes[i].ID == nodeID {
						return json.NewEncoder(os.Stdout).Encode(wf.Nodes[i])
					}
				}
				return nil
			}
			fmt.Fprintf(os.Stdout, "Node %s updated.\n", nodeID)
			return nil
		},
	}

	cmd.Flags().StringVar(&configJSON, "config", "", "New configuration as JSON object")
	cmd.Flags().StringVar(&name, "name", "", "New display name")
	cmd.Flags().Float64Var(&posX, "x", 0, "Canvas X position")
	cmd.Flags().Float64Var(&posY, "y", 0, "Canvas Y position")
	// Detect whether --x/--y were actually provided.
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		setPosX = cmd.Flags().Changed("x")
		setPosY = cmd.Flags().Changed("y")
		return nil
	}
	return cmd
}

// newWorkflowNodeRemoveCmd removes a node (and its connections) from a workflow.
func (d Deps) newNodeRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <workflow-id> <node-id>",
		Short: "Remove a node from a workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, nodeID := args[0], args[1]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), workflowID)
			expect := reviewOf(wf) // what the operator is looking at, before any edit
			if wf == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}

			newNodes := wf.Nodes[:0]
			var removed *workflow.WorkflowNode
			for i := range wf.Nodes {
				if wf.Nodes[i].ID == nodeID {
					cp := wf.Nodes[i]
					removed = &cp
					continue
				}
				newNodes = append(newNodes, wf.Nodes[i])
			}
			if removed == nil {
				return d.NotFound("node %q not found in workflow %q", nodeID, workflowID)
			}

			// Drop connections that reference the removed node.
			newConns := wf.Connections[:0]
			for _, c := range wf.Connections {
				if c.SourceNodeID == nodeID || c.TargetNodeID == nodeID {
					continue
				}
				newConns = append(newConns, c)
			}

			if err := store.SaveWorkflowGraph(approving(ctx, expect), workflowID, newNodes, newConns); err != nil {
				return fmt.Errorf("save workflow: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(removed)
			}
			fmt.Fprintf(os.Stdout, "Node %s removed.\n", nodeID)
			return nil
		},
	}
}

// ── connect / disconnect ─────────────────────────────────────────────────────

// newWorkflowConnectCmd adds an edge between two nodes.
func (d Deps) newConnectCmd() *cobra.Command {
	var fromStr, toStr string

	cmd := &cobra.Command{
		Use:   "connect <workflow-id>",
		Short: "Connect two nodes  (--from nodeID:handle --to nodeID:handle)",
		Long: `Add a connection between two nodes.

  --from and --to accept "nodeID:handle" or just "nodeID" (handle defaults to "main").

  Example:
    monoagentcli workflow connect wf1 --from abc123:main --to def456:main`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID := args[0]

			srcID, srcHandle, err := parseNodeHandle(fromStr)
			if err != nil {
				return fmt.Errorf("--from: %w", err)
			}
			dstID, dstHandle, err := parseNodeHandle(toStr)
			if err != nil {
				return fmt.Errorf("--to: %w", err)
			}

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), workflowID)
			expect := reviewOf(wf) // what the operator is looking at, before any edit
			if wf == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}

			conn := workflow.WorkflowConnection{
				ID:           uuid.New().String(),
				WorkflowID:   workflowID,
				SourceNodeID: srcID,
				SourceHandle: srcHandle,
				TargetNodeID: dstID,
				TargetHandle: dstHandle,
				Position:     len(wf.Connections),
			}
			wf.Connections = append(wf.Connections, conn)

			if err := store.SaveWorkflowConnections(approving(ctx, expect), workflowID, wf.Connections); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}

			if d.JSONOutput() {
				return json.NewEncoder(os.Stdout).Encode(conn)
			}
			fmt.Fprintf(os.Stdout, "Connected %s:%s → %s:%s  (id: %s)\n",
				srcID, srcHandle, dstID, dstHandle, conn.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromStr, "from", "", "Source node: nodeID[:handle]  (required)")
	cmd.Flags().StringVar(&toStr, "to", "", "Target node: nodeID[:handle]  (required)")
	_ = cmd.MarkFlagRequired("from")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// newWorkflowDisconnectCmd removes a connection by its ID.
func (d Deps) newDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <workflow-id> <connection-id>",
		Short: "Remove a connection from a workflow",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			workflowID, connID := args[0], args[1]

			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()

			store := d.WorkflowStore(db)
			ctx := context.Background()

			wf := d.OwnedWorkflow(ctx, store, db.DB, d.ProfileID(), workflowID)
			expect := reviewOf(wf) // what the operator is looking at, before any edit
			if wf == nil {
				return d.NotFound("workflow %q not found", workflowID)
			}

			newConns := wf.Connections[:0]
			found := false
			for _, c := range wf.Connections {
				if c.ID == connID {
					found = true
					continue
				}
				newConns = append(newConns, c)
			}
			if !found {
				return fmt.Errorf("connection %q not found in workflow %q", connID, workflowID)
			}

			if err := store.SaveWorkflowConnections(approving(ctx, expect), workflowID, newConns); err != nil {
				return fmt.Errorf("save connections: %w", err)
			}
			fmt.Fprintf(os.Stdout, "Connection %s removed.\n", connID)
			return nil
		},
	}
}

// parseNodeHandle parses "nodeID:handle" or "nodeID" (defaulting handle to "main").
func parseNodeHandle(s string) (nodeID, handle string, err error) {
	if s == "" {
		return "", "", fmt.Errorf("value is required")
	}
	parts := strings.SplitN(s, ":", 2)
	nodeID = parts[0]
	if len(parts) == 2 && parts[1] != "" {
		handle = parts[1]
	} else {
		handle = "main"
	}
	return nodeID, handle, nil
}

// approving makes the change that follows also record the workflow as the operator's own, in the
// same commit, unless an AI is the one editing.
func approving(ctx context.Context, expect string) context.Context {
	if operator.Marker() != "" {
		return ctx
	}
	// The approval covers what the operator read: if the workflow changed since, the edit is
	// refused instead of approving someone else's change along with it.
	return workflow.WithExpectHash(workflow.WithApproval(ctx), expect)
}

// reviewOf is the review hash of a workflow as it was read ("" for no workflow at all).
func reviewOf(wf *workflow.Workflow) string {
	if wf == nil {
		return ""
	}
	h, err := workflow.ReviewHash(wf)
	if err != nil {
		// Never an empty guard: an empty one would let the edit (and its approval) through unchecked.
		return unreadableReview
	}
	return h
}

// unreadableReview is a guard that matches no workflow, so an edit whose review hash could not be
// worked out is refused instead of going through unguarded.
const unreadableReview = "unreadable-review-hash"
