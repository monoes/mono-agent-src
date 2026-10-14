package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ── Execution ──────────────────────────────────────────────────────────────

// runUnifiedCLI executes monoagentcli with the given full argv and returns
// the frontend JSON string. label is used for the Logs page only.
func (a *App) runUnifiedCLI(label string, fullArgs []string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	parent := a.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, orgCLITimeout)
	defer cancel()
	a.emitLog("ORG", "INFO", fmt.Sprintf("$ %s %s", cliBin, strings.Join(fullArgs, " ")))
	startedAt := time.Now()
	cmd := exec.CommandContext(ctx, cliBin, fullArgs...)
	suppressConsole(cmd)
	out, runErr := cmd.Output()
	elapsed := time.Since(startedAt).Round(time.Millisecond)
	res := cliResultJSON(cliBin, out, runErr)
	if runErr != nil {
		a.emitLog("ORG", "ERROR", fmt.Sprintf("%s failed after %s: %s", label, elapsed, res))
	} else {
		a.emitLog("ORG", "INFO", fmt.Sprintf("%s finished in %s", label, elapsed))
	}
	return res
}

// runOrgSub runs one `org …` subcommand built by a pure builder.
func (a *App) runOrgSub(label string, sub []string, buildErr error) string {
	if buildErr != nil {
		return aiError(buildErr)
	}
	return a.runUnifiedCLI("org "+label, orgCLIArgs(a.getActiveProfileID(), a.orgProjectRoot(), sub))
}

// ── Bindings (contracts §5) ────────────────────────────────────────────────

func (a *App) ListOrgAutomations(org string) string {
	sub, err := automationListArgs(org)
	return a.runOrgSub("automation list", sub, err)
}

func (a *App) ListUnassignedAutomations() string {
	return a.runOrgSub("automation unassigned", []string{"automation", "unassigned"}, nil)
}

func (a *App) AddOrgAutomation(org, workflowID, alias string) string {
	sub, err := automationAddArgs(org, workflowID, alias)
	return a.runOrgSub("automation add", sub, err)
}

func (a *App) RemoveOrgAutomation(org, alias string) string {
	sub, err := automationRemoveArgs(org, alias)
	return a.runOrgSub("automation remove", sub, err)
}

func (a *App) ListOrgGrants(org string) string {
	sub, err := grantListArgs(org)
	return a.runOrgSub("grant list", sub, err)
}

func (a *App) SetOrgGrant(org, specJSON string) string {
	sub, err := grantAddArgs(org, specJSON)
	return a.runOrgSub("grant add", sub, err)
}

func (a *App) RemoveOrgGrant(org, role, alias string) string {
	sub, err := grantRemoveArgs(org, role, alias)
	return a.runOrgSub("grant remove", sub, err)
}

func (a *App) AddAutomationRole(org, specJSON string) string {
	sub, err := automationRoleAddArgs(org, specJSON)
	return a.runOrgSub("automation-role add", sub, err)
}

func (a *App) RemoveAutomationRole(org, roleID string) string {
	sub, err := automationRoleRemoveArgs(org, roleID)
	return a.runOrgSub("automation-role remove", sub, err)
}

func (a *App) GetEffectiveTools(org, roleID string) string {
	sub, err := effectiveToolsArgs(org, roleID)
	return a.runOrgSub("effective-tools", sub, err)
}

func (a *App) GetOrgAutonomy(org string) string {
	sub, err := autonomyShowArgs(org)
	return a.runOrgSub("autonomy show", sub, err)
}

func (a *App) SetOrgAutonomy(org, specJSON string) string {
	sub, err := autonomySetArgs(org, specJSON)
	return a.runOrgSub("autonomy set", sub, err)
}

func (a *App) PauseOrgAutonomy(org, duration string) string {
	sub, err := autonomyPauseArgs(org, duration)
	return a.runOrgSub("autonomy pause", sub, err)
}

func (a *App) ResumeOrgAutonomy(org string) string {
	sub, err := autonomyResumeArgs(org)
	return a.runOrgSub("autonomy resume", sub, err)
}

func (a *App) ListOrgDecisionLog(org, run string) string {
	sub, err := autonomyDecisionsArgs(org, run)
	return a.runOrgSub("autonomy decisions", sub, err)
}

func (a *App) ListNeedsYou(org string) string {
	sub, err := needsYouArgs(org)
	return a.runOrgSub("autonomy needs-you", sub, err)
}

func (a *App) StartOrgGroup(holding string) string {
	sub, err := groupArgs("start", holding)
	return a.runOrgSub("group start", sub, err)
}

func (a *App) StopOrgGroup(holding string) string {
	sub, err := groupArgs("stop", holding)
	return a.runOrgSub("group stop", sub, err)
}

func (a *App) OrgGroupStatus(holding string) string {
	sub, err := groupArgs("status", holding)
	return a.runOrgSub("group status", sub, err)
}

func (a *App) SendOrgMessage(org, specJSON string) string {
	sub, err := sendArgs(org, specJSON)
	return a.runOrgSub("send", sub, err)
}

// GetDaemonStatus reports whether `monoagentcli daemon` (workflow engine +
// endpoint receiver) and `org serve` are up — the "engine offline" badge on
// automation roles reads daemon.running.
func (a *App) GetDaemonStatus() string {
	return a.runUnifiedCLI("status", statusCLIArgs(a.getActiveProfileID()))
}
