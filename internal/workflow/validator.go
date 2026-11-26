package workflow

import (
	"fmt"
	"strings"
)

// DeprecatedNodeTypes lists node types that fail fast with a migration hint:
// the local-agent transition (docs/plans/local-agent-monomind-delegation.md D5)
// and removed features (removedNodeTypes). The value is the per-type hint.
var DeprecatedNodeTypes = map[string]string{
	"ai.chat":      `replace it with the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask")`,
	"ai.extract":   `replace it with the "agent.ask" node whose prompt requests JSON extraction`,
	"ai.classify":  `replace it with the "ai.choose" node (TypeSafe Jev; see "monoagentcli ref node ai.choose") or an "agent.ask" node whose prompt requests classification`,
	"ai.transform": `replace it with the "agent.ask" node`,
	"ai.agent":     `replace it with the "agent.ask" node`,
	"ai.embed":     `no local-agent equivalent exists — remove the node, or approximate via an "agent.ask" prompt (no true embeddings API)`,
	// Called an LLM over HTTP with its own API key (text, and images through
	// the same chat endpoint).
	"service.openrouter":      `replace it with the "agent.ask" node (local AI agent via monomind; see "monoagentcli ref node agent.ask"); for images use "service.huggingface" with operation generate_image`,
	"image.remove_background": `background removal was removed from monoagent and nothing replaces it: delete this node from the workflow (the other image.* nodes still work)`,
}

// removedNodeTypes are the DeprecatedNodeTypes whose feature is gone rather
// than moved to a local agent: their hint is the whole explanation.
var removedNodeTypes = map[string]bool{"image.remove_background": true}

// IsDeprecatedNodeType reports whether a node type is deprecated, returning
// its migration hint.
func IsDeprecatedNodeType(nodeType string) (string, bool) {
	hint, ok := DeprecatedNodeTypes[nodeType]
	return hint, ok
}

// DeprecatedNodeError is the error a deprecated node's executor fails with:
// the type and its migration hint.
func DeprecatedNodeError(nodeType string) error {
	hint, _ := IsDeprecatedNodeType(nodeType)
	if removedNodeTypes[nodeType] {
		return fmt.Errorf("node %q: %s", nodeType, hint)
	}
	return fmt.Errorf("node %q is deprecated by the local-agent transition — %s (docs/plans/local-agent-monomind-delegation.md)", nodeType, hint)
}

// ValidateForSave checks a workflow definition before saving.
// Returns a descriptive error for each violation (returns first error found).
// Rules:
//   - workflow name must not be empty
//   - node count must be 1–200
//   - connection count must be 0–500
//   - all node IDs must be unique within the workflow
//   - all connection source/target node IDs must reference existing nodes
//   - no cycles (uses BuildDAG which calls TopologicalSort)
//   - no node type may be empty string
//   - each connection source_handle must be non-empty
func ValidateForSave(w *Workflow) error {
	if w.Name == "" {
		return fmt.Errorf("workflow: name must not be empty")
	}

	nodeCount := len(w.Nodes)
	if nodeCount < 1 || nodeCount > 200 {
		return fmt.Errorf("workflow: node count must be between 1 and 200, got %d", nodeCount)
	}

	connCount := len(w.Connections)
	if connCount < 0 || connCount > 500 {
		return fmt.Errorf("workflow: connection count must be between 0 and 500, got %d", connCount)
	}

	// All node IDs must be unique.
	nodeIDs := make(map[string]struct{}, nodeCount)
	for _, n := range w.Nodes {
		if _, exists := nodeIDs[n.ID]; exists {
			return fmt.Errorf("workflow: duplicate node ID %q", n.ID)
		}
		nodeIDs[n.ID] = struct{}{}
	}

	// No node type may be empty; deprecated ai.* types fail fast with a hint.
	for _, n := range w.Nodes {
		if n.Type == "" {
			return fmt.Errorf("workflow: node %q has empty type", n.ID)
		}
		if hint, deprecated := IsDeprecatedNodeType(n.Type); deprecated {
			return fmt.Errorf("workflow: node %q uses deprecated type %q — %s", n.ID, n.Type, hint)
		}
	}

	// All connection source/target node IDs must reference existing nodes,
	// and source_handle must be non-empty.
	for _, c := range w.Connections {
		if _, ok := nodeIDs[c.SourceNodeID]; !ok {
			return fmt.Errorf("workflow: connection %q references unknown source node %q", c.ID, c.SourceNodeID)
		}
		if _, ok := nodeIDs[c.TargetNodeID]; !ok {
			return fmt.Errorf("workflow: connection %q references unknown target node %q", c.ID, c.TargetNodeID)
		}
		if c.SourceHandle == "" {
			return fmt.Errorf("workflow: connection %q has empty source_handle", c.ID)
		}
	}

	// No cycles — BuildDAG runs TopologicalSort internally.
	if _, err := BuildDAG(w.Nodes, w.Connections); err != nil {
		return err
	}

	return nil
}

// ValidateForActivation checks a workflow before activation.
// Rules:
//   - at least one trigger node must be present (type starts with "trigger.")
//   - every trigger.schedule node must have a non-empty "cron" config field
//   - every trigger.webhook node must have a non-empty "path" config field
//   - no required config fields for non-trigger nodes (those are runtime errors)
func ValidateForActivation(w *Workflow) error {
	if err := ValidateForSave(w); err != nil {
		return err
	}

	hasTrigger := false
	for _, n := range w.Nodes {
		if strings.HasPrefix(n.Type, "trigger.") {
			hasTrigger = true

			switch n.Type {
			case "trigger.schedule":
				cron, _ := n.Config["cron"].(string)
				if cron == "" {
					return fmt.Errorf("workflow: trigger.schedule node %q missing required config field \"cron\"", n.ID)
				}
			case "trigger.webhook":
				path, _ := n.Config["path"].(string)
				if path == "" {
					return fmt.Errorf("workflow: trigger.webhook node %q missing required config field \"path\"", n.ID)
				}
			}
		}
	}

	if !hasTrigger {
		return ErrNoTriggerNode
	}

	return nil
}
