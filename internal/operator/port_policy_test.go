package operator

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The policy operator's init() installs on the extension package, with the
// proof out of the picture (PortOverride is read directly): marker => ignored,
// in-turn verdict => ignored, neither => honoured.
func TestInitInstallsThePortEnvPolicy(t *testing.T) {
	enforcePortPolicyInTest = true
	t.Cleanup(func() { enforcePortPolicyInTest = false })
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

// In a go-test binary the variable is honoured even for a marked process unless
// the test enforces the policy: tests isolate themselves from the real bridge
// with it, and an ignored override would send them to 9222/9323.
func TestPortEnvIsHonouredInATestBinaryByDefault(t *testing.T) {
	t.Setenv(extension.ExtensionPortEnv, "9400")
	t.Setenv(workflow.EnvWorkflowRun, "run-1")
	if got := extension.PortOverride(); got != "9400" {
		t.Fatalf("a marked process in a test binary: override = %q, want it honoured", got)
	}
}

// scripts/lib/mono.sh clears the markers a script's private-bridge run must not
// carry; its list is operator.MarkerEnv().
func TestMonoShMarkerListMatches(t *testing.T) {
	b, err := os.ReadFile("../../scripts/lib/mono.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)for v in (.*?); do`).FindSubmatch(b)
	if m == nil {
		t.Fatal("mono_private_operator_env's marker loop not found in scripts/lib/mono.sh")
	}
	got := strings.Fields(strings.ReplaceAll(string(m[1]), "\\\n", " "))
	want := MarkerEnv()
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("scripts/lib/mono.sh clears %v, operator.MarkerEnv() is %v", got, want)
	}
}
