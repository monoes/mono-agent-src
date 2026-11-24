package health

import (
	"strings"
	"testing"
)

// R28-5: doctor says plainly that a registered MCP server can mutate.
func TestCheckMCPRuntimesSaysMutationsAreOn(t *testing.T) {
	res := checkMCPRuntimes([]MCPRuntime{{ID: "claude", Label: "Claude Code", Registered: true, Current: true, Where: "x"}})
	if !strings.Contains(res.Summary, "mutations ON") || !strings.Contains(res.Children[0].Summary, "mutations ON") {
		t.Fatalf("%+v", res)
	}
}
