package operator

import (
	"os"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ApprovedWorkflowChild reports whether this process carries no mark but
// that of a workflow node's child (workflow.EnvWorkflowRun) in a run nobody
// unapproved: no workflow.EnvUnapprovedRun, here or in a parent process (a
// child can drop its own copy of a variable, not its parents'), no chat mark
// and no agent marker. The send gate holds the sends of every caller that is
// not the user's terminal except this one: an approved workflow's own
// commands send as the workflow was written.
func ApprovedWorkflowChild() bool {
	if os.Getenv(workflow.EnvWorkflowRun) == "" || os.Getenv(workflow.EnvUnapprovedRun) != "" || os.Getenv(browserctl.EnvAICaller) != "" {
		return false
	}
	if _, agent := hilsend.Detect(); agent || browserctl.AgentKind() != "" {
		return false
	}
	return Marker() == workflow.EnvWorkflowRun && !unapprovedAncestor()
}
