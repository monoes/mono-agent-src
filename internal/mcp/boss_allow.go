package mcp

import "strings"

// BossToolsEnv makes a server serve the session_boss_* tools although it was not started with --allow-mutations. The chat
// child sets it for a boss's turns only (internal/cli/chat), so the MCP server a coder runtime starts for that turn lists them
// and nothing else that mutates: the workflow, vault, secret and org verbs stay hidden. The variable lists and permits the
// tools, it does not authenticate: every boss tool still verifies the calling lead's session token and that a run is going.
const BossToolsEnv = "MONOAGENT_MCP_BOSS_TOOLS"

// isBossTool says whether a tool is one of the boss's typed operations.
func isBossTool(name string) bool { return strings.HasPrefix(name, "session_boss_") }

// mutationAllowed says whether this server may serve the tool: any tool when mutations are allowed, a boss tool also when
// only the boss tools are.
func (s *Server) mutationAllowed(t tool) bool {
	return !t.mutating || s.opts.AllowMutations || (s.opts.AllowBossTools && isBossTool(t.name))
}

// listedTools is the served tools with the boss tools unmarked as mutating when only they are allowed, for the tools/list gate.
func (s *Server) listedTools() []tool {
	served := s.servedTools()
	if s.opts.AllowMutations || !s.opts.AllowBossTools {
		return served
	}
	out := make([]tool, len(served))
	for i, t := range served {
		if isBossTool(t.name) {
			t.mutating = false
		}
		out[i] = t
	}
	return out
}
