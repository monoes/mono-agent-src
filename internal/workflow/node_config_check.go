package workflow

import (
	"fmt"
	"sync/atomic"
)

// NodeConfigCheck checks a node's config ahead of a run, past its schema:
// the keys of an API package's collection params, which a run refuses
// when the package doesn't declare them (apipkg.CheckConfig, installed by
// that package). A nil check checks nothing.
type NodeConfigCheck func(nodeType string, config map[string]interface{}) error

var nodeConfigCheck atomic.Pointer[NodeConfigCheck]

// SetNodeConfigCheck installs c for the process; nil removes it.
func SetNodeConfigCheck(c NodeConfigCheck) {
	if c == nil {
		nodeConfigCheck.Store(nil)
		return
	}
	nodeConfigCheck.Store(&c)
}

// NodeConfigIssues lists each enabled node of nodes whose config the
// installed check refuses, so `workflow validate`, activation and the n8n
// import report it before a run does: the check's message, with the node.
func NodeConfigIssues(nodes []WorkflowNode) []ValidationIssue {
	p := nodeConfigCheck.Load()
	if p == nil {
		return nil
	}
	var out []ValidationIssue
	for _, n := range nodes {
		if n.Disabled {
			continue
		}
		if n.Config == nil && n.ConfigRaw != "" {
			_ = n.ParseConfig()
		}
		if err := (*p)(n.Type, n.Config); err != nil {
			out = append(out, ValidationIssue{Code: IssueUnknownField, Severity: "error", NodeID: n.ID, Message: err.Error()})
		}
	}
	return out
}

// nodeConfigError is NodeConfigIssues' first issue as an error naming the
// node, for activation; nil when there is none.
func nodeConfigError(nodes []WorkflowNode) error {
	for _, is := range NodeConfigIssues(nodes) {
		name := is.NodeID
		for _, n := range nodes {
			if n.ID == is.NodeID && n.Name != "" && n.Name != n.ID {
				name = fmt.Sprintf("%s (%s)", is.NodeID, n.Name)
			}
		}
		return fmt.Errorf("node %s: %s", name, is.Message)
	}
	return nil
}
