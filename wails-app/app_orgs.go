package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// ─────────────────────────────────────────────────────────────────────────────
// Orgs (monomind Org Runtime v2, via the Agent Exec Protocol's org surface)
//
// Same doctrine as the agent chat bindings in app_ai.go: this file never
// imports internal/monomind — every call shells out to `monoagentcli org
// <sub> ...`, whose stdout JSON is the UI contract. The Wails methods here
// return that JSON verbatim as a string; the frontend parses it (same
// pattern as ScanAgentRuntimes).
//
// Per-profile scoping: `monoagentcli org` resolves org state under
// `--project <root>/.monomind/orgs/` (see cmd/monoagentcli/org.go), NOT via
// the subprocess's cwd — so scoping to the active profile means passing
// `--project <profile root>`, not setting cmd.Dir. orgProjectRoot below
// resolves that root and defensively ensures the folder exists first
// (profiledir.EnsureLayout), falling back to the CLI's own default project
// root (unscoped, today's global behavior) if that fails.
// ─────────────────────────────────────────────────────────────────────────────

// orgCLITimeout bounds one-shot org observe/action calls from the UI.
const orgCLITimeout = 60 * time.Second

// orgProjectRoot resolves the active profile's project root for org state
// (`<root>/.monomind/orgs/`) and ensures the folder layout exists. Returns
// "" if the profile's layout can't be created, so callers leave --project off and the CLI resolves
// the root from --profile.
func (a *App) orgProjectRoot() string {
	profileID, err := a.profileID()
	if err != nil {
		return ""
	}
	if err := profiledir.EnsureLayout(a.db, profileID); err != nil {
		a.emitLog("ORG", "WARN", fmt.Sprintf("could not prepare profile folder for %q: %v (leaving the project root to the CLI)", profileID, err))
		return ""
	}
	return profiledir.Root(a.db, profileID)
}

// restrictFileWriteForOrgs reports whether newly-created org roles should
// get the default taxonomy-scoped fileWrite policy (see
// orgdesign.NewOrgOptions.RestrictFileWrite / applyRoleDefaults) — true only
// when there's an active, default-managed profile (no profiles.root_dir
// override — profiledir.IsDefaultManaged). Mirrors orgProjectRoot's own
// branching deliberately: unlike internal/ai/chat's MonoagentTools.profileRoot,
// this has no extra override/legacy-fallback branch, so the check is just
// "is there a profile, and is it unmanaged-custom."
func (a *App) restrictFileWriteForOrgs() bool {
	profileID, err := a.profileID()
	if err != nil {
		return false
	}
	return profiledir.IsDefaultManaged(a.db, profileID)
}

// runOrgCLI runs `monoagentcli org <args...>` and returns its stdout JSON
// verbatim, or an {"error":"..."} payload on failure (aiError shape,
// app_ai.go) so the frontend's existing error-guard idiom applies unchanged.
func (a *App) runOrgCLI(args ...string) string {
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, orgCLITimeout)
	defer cancel()
	projectRoot := a.orgProjectRoot()
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	fullArgs, err := orgCLIRunArgs(pid, projectRoot, args...)
	if err != nil {
		return aiError(err)
	}
	logSuffix := ""
	if projectRoot != "" {
		logSuffix = fmt.Sprintf(" (project: %s)", projectRoot)
	}
	a.emitLog("ORG", "INFO", fmt.Sprintf("$ %s %s%s", cliBin, strings.Join(fullArgs, " "), logSuffix))
	startedAt := time.Now()
	cmd := exec.CommandContext(ctx, cliBin, fullArgs...)
	suppressConsole(cmd)
	out, err := cmd.Output()
	elapsed := time.Since(startedAt).Round(time.Millisecond)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && cliStderr(ee.Stderr) != "" {
			stderr := cliStderr(ee.Stderr)
			a.emitLog("ORG", "ERROR", fmt.Sprintf("org %s failed after %s: %s", strings.Join(args, " "), elapsed, stderr))
			return aiError(fmt.Errorf("%s", stderr))
		}
		a.emitLog("ORG", "ERROR", fmt.Sprintf("org %s failed after %s: %v", strings.Join(args, " "), elapsed, err))
		return aiError(err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		a.emitLog("ORG", "WARN", fmt.Sprintf("org %s returned empty output after %s", strings.Join(args, " "), elapsed))
		return aiError(fmt.Errorf("org %s: empty output", strings.Join(args, " ")))
	}
	if !looksLikeJSON(trimmed) {
		err := notJSONError(cliBin, trimmed)
		a.emitLog("ORG", "ERROR", fmt.Sprintf("org %s: %v", strings.Join(args, " "), err))
		return aiError(err)
	}
	a.emitLog("ORG", "INFO", fmt.Sprintf("org %s finished in %s", strings.Join(args, " "), elapsed))
	return trimmed
}

func (a *App) ListOrgs() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("list")
}

func (a *App) GetOrgStatus(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if name == "" {
		return a.runOrgCLI("status")
	}
	return a.runOrgCLI("status", name)
}

// run, when non-empty, scopes to that specific run id instead of
// monomind's own default (the most recent run).
func (a *App) GetOrgLogs(name, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if run != "" {
		return a.runOrgCLI("logs", name, "--run", run)
	}
	return a.runOrgCLI("logs", name)
}

// run is ignored when all=true (report --all already covers every run).
func (a *App) GetOrgReport(name string, all bool, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if all {
		return a.runOrgCLI("report", name, "--all")
	}
	if run != "" {
		return a.runOrgCLI("report", name, "--run", run)
	}
	return a.runOrgCLI("report", name)
}

func (a *App) GetOrgCosts(name, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if run != "" {
		return a.runOrgCLI("costs", name, "--run", run)
	}
	return a.runOrgCLI("costs", name)
}

// GetOrgBudget is the per-section spend, allocation, role caps and soft
// closure of a run (`monoagentcli org budget`); run empty = current run.
func (a *App) GetOrgBudget(name, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if run != "" {
		return a.runOrgCLI("budget", name, "--run", run)
	}
	return a.runOrgCLI("budget", name)
}

// GetOrgEstimate is monomind's pre-run cost estimate with its "stale rates"
// note (`monoagentcli org estimate`); an error payload when unavailable.
func (a *App) GetOrgEstimate(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("estimate", name)
}

func (a *App) GetOrgFlow(name, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if run != "" {
		return a.runOrgCLI("flow", name, "--run", run)
	}
	return a.runOrgCLI("flow", name)
}

func (a *App) GetOrgQuestions(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("questions", name)
}

// GetOrgApprovals returns pending tool/action approval requests — the
// queue that actually gates Bash/WebFetch/WebSearch/org_complete, distinct
// from and not resolved by GetOrgQuestions/GetOrgGates.
func (a *App) GetOrgApprovals(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("approvals", name)
}

func (a *App) GetOrgGates(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("gates", name)
}

func (a *App) GetOrgDecisions(name, run string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if run != "" {
		return a.runOrgCLI("decisions", name, "--run", run)
	}
	return a.runOrgCLI("decisions", name)
}

func (a *App) GetOrgMemoryStats(name string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("memory", name)
}

func (a *App) AnswerOrgQuestion(name, questionID, answer string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("answer", name, questionID, answer)
}

func (a *App) GateApproveOrgAction(name, gateID, resolution string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if resolution == "" {
		return a.runOrgCLI("gate-approve", name, gateID)
	}
	return a.runOrgCLI("gate-approve", name, gateID, resolution)
}

func (a *App) GateRejectOrgAction(name, gateID, resolution string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if resolution == "" {
		return a.runOrgCLI("gate-reject", name, gateID)
	}
	return a.runOrgCLI("gate-reject", name, gateID, resolution)
}
