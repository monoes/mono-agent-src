package hilsend

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/workflow"
)

// A send held under a workflow run's mark says why: no approved run token.
func TestHeldSendNamesTheMissingRunToken(t *testing.T) {
	t.Setenv(workflow.EnvWorkflowRun, "")
	if got := heldByRunToken(); got != "" {
		t.Errorf("outside a workflow run: %q", got)
	}
	t.Setenv(workflow.EnvWorkflowRun, "exec-1")
	if got := heldByRunToken(); !strings.Contains(got, "run token") {
		t.Errorf("under a workflow run: %q", got)
	}
}
