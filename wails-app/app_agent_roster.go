package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// ─────────────────────────────────────────────────────────────────────────────
// Validated agent roster (monoes/mono-agent#225). Everything shells out to
// `monoagentcli agent validate|roster`; a validation run streams its NDJSON
// progress lines to the frontend as "agents:validate" events.
// ─────────────────────────────────────────────────────────────────────────────

const agentValidateKey = "agentvalidate"

// AgentRoster returns `agent roster --json`: every runtime's models with
// their state (ready, stale, failed, untested). No model calls.
func (a *App) AgentRoster() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "roster")
}

// AgentValidatePlan returns the validate.plan line of a dry run: the calls a
// validation would make and their estimated cost.
func (a *App) AgentValidatePlan(runtimes, models []string, staleOnly bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult(validateArgs(runtimes, models, staleOnly, true)...)
}

// StartAgentValidation runs `agent validate` in the background. Each progress
// line is emitted as "agents:validate"; when the process ends,
// "agents:validateClosed" follows. A second start replaces a running one.
func (a *App) StartAgentValidation(runtimes, models []string, staleOnly bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	args := append([]string{"--json"}, validateArgs(runtimes, models, staleOnly, false)...)
	return a.startAgentValidation(cliBin, args, func(name string, data interface{}) {
		eventsEmit(a.ctx, name, data)
	})
}

func (a *App) startAgentValidation(cliBin string, args []string, emit func(string, interface{})) string {
	a.emitLog("AI", "INFO", fmt.Sprintf("$ %s %s", cliBin, strings.Join(args, " ")))
	cmd := exec.Command(cliBin, args...)
	setChatProcessGroup(cmd)
	suppressConsole(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return aiError(err)
	}
	stderr := a.chatLogWriter()
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return aiError(fmt.Errorf("start agent validate: %w", err))
	}

	a.runningMu.Lock()
	if prev, ok := a.runningCmds[agentValidateKey]; ok {
		killChatProcessGroup(prev)
	}
	a.runningCmds[agentValidateKey] = cmd
	a.runningMu.Unlock()

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			emit("agents:validate", json.RawMessage(append([]byte(nil), line...)))
		}
		err := waitChatProcess(cmd)
		a.runningMu.Lock()
		if a.runningCmds[agentValidateKey] == cmd {
			delete(a.runningCmds, agentValidateKey)
		}
		a.runningMu.Unlock()
		emit("agents:validateClosed", validateClosed(err, stderr.lastLine()))
	}()
	return `{"ok":true}`
}

// validateClosed is the "agents:validateClosed" payload. A failed run
// carries the CLI's own error, its last stderr line (e.g. "validation
// cancelled after 2 of 6 tests" after Stop), not the bare "exit status 1".
func validateClosed(err error, lastStderr string) map[string]interface{} {
	closed := map[string]interface{}{"ok": err == nil}
	if err != nil {
		msg := err.Error()
		if lastStderr != "" {
			msg = lastStderr
		}
		closed["error"] = msg
	}
	return closed
}

// StopAgentValidation cancels a running validation. Results already stored
// are kept.
func (a *App) StopAgentValidation() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	a.runningMu.Lock()
	cmd, ok := a.runningCmds[agentValidateKey]
	a.runningMu.Unlock()
	if !ok {
		return `{"ok":false,"error":"no validation running"}`
	}
	killChatProcessGroup(cmd)
	return `{"ok":true}`
}

// AgentRosterAdd adds a model id a runtime doesn't list.
func (a *App) AgentRosterAdd(runtimeID, model string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "roster", "add", runtimeID, model)
}

// AgentRosterRemove removes a model from the roster.
func (a *App) AgentRosterRemove(runtimeID, model string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "roster", "remove", runtimeID, model)
}

// AgentRosterAutoRevalidate returns `agent roster auto-revalidate status
// --json` (#230): the setting, today's runs and spend, and what the next
// automatic run would test with its estimated cost. No model calls, and no
// scan (the roster refresh already runs one), so the estimate covers
// age staleness only.
func (a *App) AgentRosterAutoRevalidate() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.jsonResult("agent", "roster", "auto-revalidate", "status", "--no-scan")
}

// SetAgentRosterAutoRevalidate turns automatic re-validation on or off. The
// frontend turns it on only after the user confirmed that it costs money.
func (a *App) SetAgentRosterAutoRevalidate(on bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	state := "off"
	if on {
		state = "on"
	}
	return a.jsonResult("agent", "roster", "auto-revalidate", state)
}

func validateArgs(runtimes, models []string, staleOnly, dry bool) []string {
	args := []string{"agent", "validate"}
	for _, r := range runtimes {
		args = append(args, "--runtime", r)
	}
	for _, m := range models {
		args = append(args, "--model", m)
	}
	if staleOnly {
		args = append(args, "--stale-only")
	}
	if dry {
		args = append(args, "--dry-run")
	}
	return args
}
