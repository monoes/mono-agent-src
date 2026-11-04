package operator

import (
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The policy operator's init() installs on the extension package, with the
// proof out of the picture (PortOverride is read directly): marker => ignored,
// in-turn verdict => ignored, neither => honoured.
func TestInitInstallsThePortEnvPolicy(t *testing.T) {
	t.Setenv(extension.ExtensionPortEnv, "9400")
	if got := extension.PortOverride(); got != "9400" {
		t.Fatalf("operator: override = %q, want 9400 (honoured)", got)
	}
	t.Run("marker", func(t *testing.T) {
		t.Setenv(workflow.EnvWorkflowRun, "run-1")
		if got := extension.PortOverride(); got != "" {
			t.Fatalf("a marked process followed the port env: %q", got)
		}
	})
	t.Run("in-turn", func(t *testing.T) {
		SetTurnVerdict(func() string { return "a coder turn" })
		t.Cleanup(func() { SetTurnVerdict(nil) })
		if got := extension.PortOverride(); got != "" {
			t.Fatalf("an in-turn process followed the port env: %q", got)
		}
	})
}
