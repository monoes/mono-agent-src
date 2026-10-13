package health

import (
	"context"
	"fmt"
	"strings"
)

// GroupIntegrations covers how coding agents (Claude Code) reach monoagent.
const GroupIntegrations = "integrations"

const (
	CheckClaudeSkills = "integrations.claude_skills"
	CheckMCP          = "integrations.mcp_monoagent"

	FixClaudeSkills = "integrations.claude_skills.install"
	FixMCPRegister  = "integrations.mcp_monoagent.register"
)

func integrationChecks() []Check {
	return []Check{
		{ID: CheckClaudeSkills, Group: GroupIntegrations, Title: "Claude Code skills", Features: []string{"Claude Code knows monoagent"}, Run: checkClaudeSkills},
		{ID: CheckMCP, Group: GroupIntegrations, Title: "MCP in Claude Code", Features: []string{"monoagent tools inside Claude Code"}, Run: checkMCP},
	}
}

func integrationFixes() []Fix {
	return []Fix{
		{FixInfo: FixInfo{ID: FixClaudeSkills, Label: "Install / refresh monoagent's Claude Code skills", Safety: SafetyAuto,
			Command: "monoagentcli init --claude"}, Apply: func(_ context.Context, env *Env, progress func(string)) error {
			if env.InstallClaudeSkills == nil {
				return fmt.Errorf("not available here")
			}
			progress("writing skills to ~/.claude/skills")
			return env.InstallClaudeSkills()
		}},
		{FixInfo: FixInfo{ID: FixMCPRegister, Label: "Register monoagent's MCP server with the installed coding agents", Safety: SafetyConfirm,
			Command: "claude mcp add --scope user monoagent -- monoagentcli mcp --allow-mutations (and the same for Codex, Gemini CLI, opencode)", Optional: true},
			Apply: func(ctx context.Context, env *Env, progress func(string)) error {
				if env.RegisterMCP == nil {
					return fmt.Errorf("not available here")
				}
				return env.RegisterMCP(ctx, progress)
			}},
	}
}

func checkClaudeSkills(_ context.Context, env *Env) Result {
	if env.ClaudeSkills == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	found, missing, stale := env.ClaudeSkills()
	if !found {
		return Result{Status: StatusSkip, Summary: "Claude Code not installed (~/.claude missing)"}
	}
	switch {
	case len(missing) > 0:
		return Result{Status: StatusWarn, Summary: "missing: " + strings.Join(missing, ", "), FixID: FixClaudeSkills}
	case len(stale) > 0:
		return Result{Status: StatusWarn, Summary: "outdated: " + strings.Join(stale, ", "), FixID: FixClaudeSkills}
	}
	return Result{Status: StatusOK, Summary: "installed and current"}
}

func checkMCP(_ context.Context, env *Env) Result {
	if env.MCPRegistration == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	found, registered, where := env.MCPRegistration()
	if !found {
		return Result{Status: StatusSkip, Summary: "Claude Code not installed"}
	}
	if registered {
		return Result{Status: StatusOK, Summary: where}
	}
	return Result{Status: StatusInfo, Summary: "not registered (optional)", FixID: FixMCPRegister}
}
