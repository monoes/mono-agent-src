package chat

import (
	"context"
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/mcp"
)

// A boss's turn gets the variable that makes the MCP server list the boss tools; the chat's own conversation does not.
func TestBossTurnExposesTheBossTools(t *testing.T) {
	r := newSteerRig(t, true)
	t.Setenv(mcp.BossToolsEnv, "")
	if err := exposeBossTools(context.Background(), r.cfg, "default", r.front.ID); err != nil || os.Getenv(mcp.BossToolsEnv) != "" {
		t.Fatalf("a chat that is no boss's got the boss tools: %v %q", err, os.Getenv(mcp.BossToolsEnv))
	}
	if err := exposeBossTools(context.Background(), r.cfg, "default", r.bossCon.ID); err != nil || os.Getenv(mcp.BossToolsEnv) != "1" {
		t.Fatalf("the boss's turn did not get the boss tools: %v %q", err, os.Getenv(mcp.BossToolsEnv))
	}
}
