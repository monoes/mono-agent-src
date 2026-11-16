package orggrant

import (
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/workflow"
)

// ErrWorkflowChanged: the granted workflow is no longer the definition the
// grant approved.
var ErrWorkflowChanged = errors.New("the workflow changed since the grant approved it")

// WorkflowHash is the content hash a grant stores for the workflow it
// approves: the workflow's definition (name, description, settings, nodes and
// their config, connections). Activating or deactivating does not change it.
func WorkflowHash(wf *workflow.Workflow) (string, error) {
	return workflow.DefinitionHash(wf)
}

// CheckWorkflow reports whether wf is still what the grant approved. A grant
// without a stored hash predates the check and is honoured until it is next
// re-granted (`org grant`), which stores one; every other grant is honoured
// only while the workflow's current hash equals the stored one.
func (t Tool) CheckWorkflow(wf *workflow.Workflow) error {
	if t.WorkflowHash == "" {
		return nil
	}
	if wf == nil {
		return fmt.Errorf("%w: it cannot be read", ErrWorkflowChanged)
	}
	h, err := WorkflowHash(wf)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWorkflowChanged, err)
	}
	if h != t.WorkflowHash {
		return ErrWorkflowChanged
	}
	return nil
}
