package main

import (
	"encoding/json"
	"github.com/monoes/mono-agent/internal/testhome"
	"path/filepath"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// edgesOf exports a workflow through the CLI and returns its connections
// keyed by source node id.
func edgesOf(t *testing.T, cfg *globalConfig, wfID string) map[string]string {
	t.Helper()
	exported := runWorkflowSubcmd(t, cfg, "export", wfID)
	var file workflow.WorkflowFile
	if err := json.Unmarshal([]byte(exported), &file); err != nil {
		t.Fatalf("parse export output: %v (out: %q)", err, exported)
	}
	edges := make(map[string]string, len(file.Connections))
	for _, c := range file.Connections {
		edges[c.Source] = c.Target
	}
	return edges
}

// TestWorkflowNodeAddSetPreserveEdgesCLI is the end-to-end regression guard
// for the removed re-save workaround in `workflow node add` / `node set`:
// with the store-level upsert in place, adding or updating a node must keep
// every existing edge without the CLI re-saving connections.
func TestWorkflowNodeAddSetPreserveEdgesCLI(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	cfg := &globalConfig{DBPath: filepath.Join(t.TempDir(), "nodes.db"), JSONOutput: true, ProfileID: "default"}

	var created workflow.Workflow
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "create", "edges-wf")), &created); err != nil {
		t.Fatalf("parse create output: %v", err)
	}
	wfID := created.ID

	addNode := func(name string) string {
		t.Helper()
		out := runWorkflowSubcmd(t, cfg, "node", "add", wfID, "--type", "core.set", "--name", name)
		var node workflow.WorkflowNode
		if err := json.Unmarshal([]byte(out), &node); err != nil {
			t.Fatalf("parse node add output: %v (out: %q)", err, out)
		}
		return node.ID
	}
	a := addNode("A")
	b := addNode("B")
	runWorkflowSubcmd(t, cfg, "connect", wfID, "--from", a, "--to", b)
	if edges := edgesOf(t, cfg, wfID); len(edges) != 1 || edges[a] != b {
		t.Fatalf("setup: expected 1 edge %s→%s, got %v", a, b, edges)
	}

	// node add must NOT drop the existing edge (regression: the old store
	// delete-all+reinsert cascaded every edge away; the CLI used to paper
	// over it by re-saving connections — the store now preserves them).
	c := addNode("C")
	if edges := edgesOf(t, cfg, wfID); len(edges) != 1 || edges[a] != b {
		t.Fatalf("node add dropped edges: got %v, want %s→%s", edges, a, b)
	}

	// node set must keep edges too.
	runWorkflowSubcmd(t, cfg, "node", "set", wfID, b, "--name", "B-renamed")
	if edges := edgesOf(t, cfg, wfID); len(edges) != 1 || edges[a] != b {
		t.Fatalf("node set dropped edges: got %v, want %s→%s", edges, a, b)
	}

	// node remove still drops only the removed node's edges.
	runWorkflowSubcmd(t, cfg, "connect", wfID, "--from", b, "--to", c)
	runWorkflowSubcmd(t, cfg, "node", "remove", wfID, b)
	edges := edgesOf(t, cfg, wfID)
	if _, ok := edges[a]; ok {
		t.Fatalf("edge touching removed node %s survived: %v", b, edges)
	}
	if _, ok := edges[b]; ok {
		t.Fatalf("edge from removed node %s survived: %v", b, edges)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges after removing the middle node, got %v", edges)
	}

	// Sanity: the exported workflow still has the two remaining nodes.
	exported := runWorkflowSubcmd(t, cfg, "export", wfID)
	var file workflow.WorkflowFile
	if err := json.Unmarshal([]byte(exported), &file); err != nil {
		t.Fatalf("parse export output: %v", err)
	}
	if len(file.Nodes) != 2 {
		t.Fatalf("expected 2 nodes after remove, got %d", len(file.Nodes))
	}
}
