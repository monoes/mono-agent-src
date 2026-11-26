package monomind

import (
	"strings"
	"testing"
)

// Only a verdict with no sandbox at all is "missing": claude's scoped access
// and the restricted fallback still limit the turn.
func TestSandboxMissing(t *testing.T) {
	for status, want := range map[string]bool{
		"": false, SandboxStatusSandboxed: false, SandboxStatusScoped: false, SandboxStatusRestricted: false,
		SandboxStatusUnsupported: true, SandboxStatusAwaitingMonomind: true, SandboxStatusNeedsMonomind: true, SandboxStatusOff: true,
	} {
		if got := SandboxMissing(status); got != want {
			t.Errorf("SandboxMissing(%q) = %v, want %v", status, got, want)
		}
	}
	if msg := UnsandboxedWarning("copilot", SandboxStatusUnsupported); !strings.Contains(msg, "without a sandbox") || !strings.Contains(msg, "copilot") {
		t.Errorf("warning = %q", msg)
	}
}
