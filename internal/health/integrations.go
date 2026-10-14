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
		{ID: CheckMCP, Group: GroupIntegrations, Title: "MCP in coding agents", Features: []string{"monoagent tools inside Claude Code, Codex, Gemini CLI and opencode"}, Run: checkMCP},
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

func checkMCP(ctx context.Context, env *Env) Result {
	if env.MCPRuntimes != nil {
		return checkMCPRuntimes(env.MCPRuntimes())
	}
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

// checkMCPRuntimes reports one row per installed coding agent: registered
// with --allow-mutations (ok), registered without it (outdated), not
// registered (optional) or a config that cannot be read. The register fix,
// which covers every agent, is on the parent row.
func checkMCPRuntimes(rts []MCPRuntime) Result {
	if len(rts) == 0 {
		return Result{Status: StatusSkip, Summary: "no coding agent installed"}
	}
	res := Result{}
	var outdated, missing, broken []string
	for _, rt := range rts {
		child := Result{ID: CheckMCP + "." + rowID(rt.ID), Title: rt.Label}
		switch {
		case rt.Err != "":
			broken = append(broken, rt.Label)
			child.Status, child.Summary = StatusWarn, "config cannot be read: "+rt.Err
		case rt.Current:
			child.Status, child.Summary = StatusOK, rt.Where
		case rt.Registered:
			outdated = append(outdated, rt.Label)
			child.Status, child.Summary = StatusWarn, "outdated: registered without --allow-mutations ("+rt.Where+")"
		default:
			missing = append(missing, rt.Label)
			child.Status, child.Summary = StatusInfo, "not registered (optional)"
		}
		res.Children = append(res.Children, child)
	}
	switch {
	case len(outdated) > 0:
		res.Status, res.Summary, res.FixID = StatusWarn, "outdated: "+strings.Join(outdated, ", "), FixMCPRegister
	case len(broken) > 0:
		res.Status, res.Summary = StatusWarn, "config cannot be read: "+strings.Join(broken, ", ")
	case len(missing) > 0:
		res.Status, res.Summary, res.FixID = StatusInfo, "not registered with "+strings.Join(missing, ", ")+" (optional)", FixMCPRegister
	default:
		res.Status, res.Summary = StatusOK, fmt.Sprintf("registered with %d coding agent(s)", len(rts))
	}
	return res
}
