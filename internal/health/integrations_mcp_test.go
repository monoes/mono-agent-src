package health

import (
	"context"
	"strings"
	"testing"
)

func TestCheckMCPPerRuntime(t *testing.T) {
	ctx := context.Background()
	env := &Env{MCPRuntimes: func() []MCPRuntime {
		return []MCPRuntime{
			{ID: "claude", Label: "Claude Code", Registered: true, Current: true, Where: `"monoagent" in ~/.claude.json`},
			{ID: "codex", Label: "Codex", Registered: true, Where: `"monoagent" in ~/.codex/config.toml`},
			{ID: "opencode", Label: "opencode"},
		}
	}}
	res := checkMCP(ctx, env)
	if res.Status != StatusWarn || res.FixID != FixMCPRegister || !strings.Contains(res.Summary, "outdated: Codex") {
		t.Fatalf("parent = %+v, want warn/outdated with the register fix", res)
	}
	if len(res.Children) != 3 {
		t.Fatalf("want one row per runtime, got %d", len(res.Children))
	}
	want := []struct {
		id     string
		status Status
		fix    string
	}{
		{CheckMCP + ".claude", StatusOK, ""},
		{CheckMCP + ".codex", StatusWarn, ""},
		{CheckMCP + ".opencode", StatusInfo, ""},
	}
	for i, w := range want {
		c := res.Children[i]
		if c.ID != w.id || c.Status != w.status || c.FixID != w.fix {
			t.Errorf("row %d = %s %s fix %q, want %s %s fix %q", i, c.ID, c.Status, c.FixID, w.id, w.status, w.fix)
		}
	}

	env.MCPRuntimes = func() []MCPRuntime {
		return []MCPRuntime{{ID: "claude", Label: "Claude Code", Registered: true, Current: true}, {ID: "codex", Label: "Codex", Registered: true, Current: true}}
	}
	if res := checkMCP(ctx, env); res.Status != StatusOK || res.FixID != "" {
		t.Fatalf("all current = %+v, want ok without fix", res)
	}

	env.MCPRuntimes = func() []MCPRuntime { return []MCPRuntime{{ID: "gemini", Label: "Gemini CLI"}} }
	if res := checkMCP(ctx, env); res.Status != StatusInfo || !strings.Contains(res.Summary, "optional") {
		t.Fatalf("not registered = %+v, want info (optional)", res)
	}

	env.MCPRuntimes = func() []MCPRuntime { return nil }
	if res := checkMCP(ctx, env); res.Status != StatusSkip {
		t.Fatalf("no agents = %+v, want skip", res)
	}
}
