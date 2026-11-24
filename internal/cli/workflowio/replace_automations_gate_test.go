package workflowio

import (
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/testhome"
)

// An agent's `workflow import --replace-automations` is refused; a plain import is not.
func TestImportReplaceAutomationsIsOperatorOnly(t *testing.T) {
	testhome.SetHome(t, t.TempDir())
	cfg := newTestConfig(t)
	d := testDeps(t, cfg)
	d.Refusal = func() string { return "an agent is running this" }
	const wf = `{"name":"w","nodes":[{"id":"t","type":"trigger.manual","name":"T","position":{"x":0,"y":0},"config":{}}],"connections":[]}`
	if _, _, _, err := run(t, d, wf, "import", "--replace-automations"); clicore.ExitCodeFor(err) != 3 {
		t.Fatalf("--replace-automations by an agent: %v", err)
	}
	if _, _, _, err := run(t, d, wf, "import"); err != nil {
		t.Fatalf("a plain import by an agent: %v", err)
	}
}
