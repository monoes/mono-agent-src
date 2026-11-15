package workflow

import (
	"context"
	"strings"

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
	if st.Allowed() {
		return nil, nil
	}
	var paused []string
	for _, w := range wfs {
		if !w.IsActive {
			continue
		}
		full, err := store.GetWorkflow(ctx, w.ID)
		if err != nil {
			return paused, err
		}
		if full != nil && firesOnItsOwn(full) {
			paused = append(paused, w.ID)
		}
	}
	return paused, nil
}

// firesOnItsOwn reports whether wf has a trigger node that starts a run without a person: any trigger but manual.
func firesOnItsOwn(wf *Workflow) bool {
	for _, n := range wf.Nodes {
		if n.Disabled || !strings.HasPrefix(n.Type, "trigger.") || n.Type == "trigger.manual" {
			continue
		}
		return true
	}
	return false
}
