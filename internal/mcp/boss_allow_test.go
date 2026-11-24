package mcp

import (
	"context"
	"strings"
	"testing"
)

func listedNames(s *Server) map[string]bool {
	out := map[string]bool{}
	for _, d := range definitionsOf(s.listedTools(), s.opts.AllowMutations) {
		out[d["name"].(string)] = true
	}
	return out
}

// A boss's turn lists the boss tools and no other mutating tool; without the variable no boss tool is listed, and a direct call
// of a gated tool is refused.
func TestBossToolsAreListedWithoutAllowMutationsAndNothingElseIs(t *testing.T) {
	t.Setenv(BossToolsEnv, "")
	t.Setenv("MONOAGENT_MCP_ALLOW_MUTATIONS", "")
	plain := NewServer(Options{})
	for n := range listedNames(plain) {
		if isBossTool(n) {
			t.Fatalf("a server without the boss variable lists %s", n)
		}
	}
	t.Setenv(BossToolsEnv, "1")
	boss := NewServer(Options{})
	got := listedNames(boss)
	for _, n := range bossToolNames {
		if !got[n] {
			t.Errorf("the boss's server does not list %s", n)
		}
	}
	mutating := 0
	for _, tl := range allTools() {
		if tl.mutating && !isBossTool(tl.name) {
			mutating++
			if got[tl.name] {
				t.Errorf("the boss's server lists the mutating tool %s", tl.name)
			}
			if boss.mutationAllowed(tl) {
				t.Errorf("the boss's server may serve %s", tl.name)
			}
		}
	}
	if mutating == 0 {
		t.Fatal("no mutating tool to check against")
	}
	if _, err := callTool(context.Background(), boss, "workflow_run", nil); err == nil || !strings.Contains(err.Error(), "requires --allow-mutations") {
		t.Fatalf("a gated tool was called on the boss's server: %v", err)
	}
}
