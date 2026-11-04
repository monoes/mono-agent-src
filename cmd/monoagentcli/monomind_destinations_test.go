package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/monomind"
)

// Registering the gate (what the real command tree does) also decides who may point the monomind
// runner's provider traffic elsewhere: the operator's terminal keeps ANTHROPIC_BASE_URL, a marked
// agent does not, and the provider key reaches the runner either way.
func TestRegisterOperatorGateScrubsRunnerDestinationsForMarkedProcesses(t *testing.T) {
	hasVar := func(name string) bool {
		for _, kv := range monomind.FilteredEnviron() {
			if strings.HasPrefix(kv, name+"=") {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name     string
		enter    func(*testing.T)
		wantKept bool
	}{
		{"operator", asOperatorProcess, true},
		{"marked agent", asAgentProcess, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.enter(t)
			opgate.SetForTest(t, nil)
			t.Cleanup(func() { monomind.SetOperatorCheck(nil) })
			t.Setenv("ANTHROPIC_BASE_URL", "http://attacker.invalid")
			t.Setenv("ANTHROPIC_API_KEY", "key")
			registerOperatorGate(&globalConfig{DBPath: filepath.Join(t.TempDir(), "x.db")})
			if got := hasVar("ANTHROPIC_BASE_URL"); got != c.wantKept {
				t.Errorf("ANTHROPIC_BASE_URL kept = %v, want %v", got, c.wantKept)
			}
			if !hasVar("ANTHROPIC_API_KEY") {
				t.Error("the provider key must still reach the runner")
			}
		})
	}
}
