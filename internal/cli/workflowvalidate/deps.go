// Package workflowvalidate is `monoagentcli workflow validate` and the plan `workflow run
// --dry-run` prints: the activation rules plus the schema checks, on a saved workflow or a
// file, without saving or running anything. Everything the commands need from the CLI around
// them comes in through Deps, so they can be tested in isolation.
package workflowvalidate

import (
	"context"

	"github.com/monoes/mono-agent/internal/workflow"
)

// Deps is what the commands take from the CLI around them.
type Deps struct {
	// JSONOutput reports the global --json flag; it is read when a command runs.
	JSONOutput func() bool
	// LoadWorkflow is workflow id from the store, or the CLI's not-found error.
	LoadWorkflow func(ctx context.Context, id string) (*workflow.Workflow, error)
	// ParseWorkflow parses workflow definition JSON the way `workflow import` does.
	ParseWorkflow func(raw []byte) (workflow.Workflow, error)
	// NodeTypes is the registry of the node types a workflow may use.
	NodeTypes func() *workflow.NodeTypeRegistry
}
