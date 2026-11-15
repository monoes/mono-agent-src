package workflow

import (
	"context"

	"github.com/monoes/mono-agent/internal/account"
)

// WorkflowGetter loads one workflow with its nodes (SQLiteWorkflowStore and HybridWorkflowStore both are).
type WorkflowGetter interface {
	GetWorkflow(ctx context.Context, id string) (*Workflow, error)
}

// PausedTriggerWorkflows returns the ids of the active workflows among wfs that have a trigger which fires on its
// own (everything but trigger.manual) while the monoes.me sign-in does not allow work: the engine drops those
// triggers (handleTrigger), and a schedule's missed ticks are not made up. It reads the already-judged status st and
// the stored workflows only: no network, nothing written, the sign-in is not consumed.
func PausedTriggerWorkflows(ctx context.Context, store WorkflowGetter, wfs []Workflow, st account.Status) ([]string, error) {
	return nil, nil
}
