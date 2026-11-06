package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/monoes/mono-agent/internal/nodemgr"
)

// ─────────────────────────────────────────────────────────────────────────────
// Settings › System health. Everything is `monoagentcli doctor` — this file
// only runs it and relays its JSON (docs/plans/2026-09-24-setup-and-health-
// check.md §5). The one thing the GUI answers itself is "is there a CLI at
// all", since it can't ask a CLI that isn't there.
// ─────────────────────────────────────────────────────────────────────────────

const (
	healthCheckTimeout    = 4 * time.Minute  // runtime scan + monomind's own checks
	healthProjectsTimeout = 12 * time.Minute // monomind doctor per project
	healthFixTimeout      = 20 * time.Minute // installs (Node, monomind, runtimes)
)

// Health check modes. "background" is the GUI's own start-up and
// 30-minute check: it leaves out the runtimes group, whose scan runs every
// agent CLI's --version and monomind's init, and those write their own state
// outside mono-agent (#146). "local" (Check again), "deep" and "projects"
// are what the person asks for, so they may scan.
const (
	healthModeBackground = "background"
	healthModeLocal      = "local"
	healthModeDeep       = "deep"
	healthModeProjects   = "projects"
)

// healthArgs builds `--profile P --json doctor [--skip-group runtimes |
// --deep | --projects]` for a mode.
func healthArgs(profileID, mode string) ([]string, error) {
	args, err := scopedCLIArgs(profileID, "doctor")
	if err != nil {
		return nil, err
	}
	switch mode {
	case healthModeBackground:
		return append(args, "--skip-group", "runtimes"), nil
	case healthModeLocal:
		return args, nil
	case healthModeDeep:
		return append(args, "--deep"), nil
	case healthModeProjects:
		return append(args, "--projects"), nil
	}
	return nil, fmt.Errorf("unknown health check mode %q", mode)
}

// healthFixArgs builds `--profile P --json doctor fix <id>`.
func healthFixArgs(profileID, fixID string) ([]string, error) {
	// "--" so a fix id can never be read as a flag.
	return scopedCLIArgs(profileID, "doctor", "fix", "--", fixID)
}

// healthReportJSON returns the doctor report verbatim. doctor exits 1 when a
// required check fails but still prints the full report, so a report on
// stdout wins over the exit status.
func healthReportJSON(cliBin string, stdout []byte, runErr error) string {
	trimmed := strings.TrimSpace(string(stdout))
	var probe struct {
		V int `json:"v"`
	}
	if strings.HasPrefix(trimmed, "{") && json.Unmarshal([]byte(trimmed), &probe) == nil && probe.V > 0 {
		return trimmed
	}
	return cliResultJSON(cliBin, stdout, runErr)
}

// RunHealthCheck runs the checks of one mode (see healthArgs) and returns
// the doctor --json report, {"error", "cli_missing"} when there is no
// monoagentcli to ask, or {"error", "cancelled"} after CancelHealthRun.
func (a *App) RunHealthCheck(mode string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		b, _ := json.Marshal(map[string]any{"error": err.Error(), "cli_missing": true})
		return string(b)
	}
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	args, err := healthArgs(pid, mode)
	if err != nil {
		return aiError(err)
	}
	timeout := healthCheckTimeout
	if mode == healthModeProjects {
		timeout = healthProjectsTimeout
	}
	key := "check:" + mode
	run, ctx, ok := beginHealthRun(a.parentCtx(), key, timeout)
	if !ok {
		return aiError(fmt.Errorf("a %s check is already running", mode))
	}
	defer endHealthRun(key, run)
	cmd := exec.CommandContext(ctx, cliBin, args...)
	suppressConsole(cmd)
	stopGracefully(cmd)
	out, runErr := cmd.Output()
	if run.cancelled.Load() {
		return `{"error":"cancelled","cancelled":true}`
	}
	return healthReportJSON(cliBin, out, runErr)
}

func (a *App) parentCtx() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// healthRun is one running check or fix, so the person can cancel it.
type healthRun struct {
	cancel    context.CancelFunc
	cancelled atomic.Bool
}

var (
	healthRunsMu sync.Mutex
	healthRuns   = map[string]*healthRun{} // runKey → the run
)

// runKey is the lock (and cancel) key of a run. Both ways of installing a
// runtime, the AI agents page (agent.install:<id>) and the health runtime
// fix (runtimes.install:<id>), share one key, so they can't overlap: two npm
// installs into one global folder, or two vendor scripts at once.
func runKey(id string) string {
	for _, prefix := range []string{"agent.install:", "runtimes.install:"} {
		if rt, ok := strings.CutPrefix(id, prefix); ok {
			return "runtime:" + rt
		}
	}
	return id
}

// beginHealthRun registers a run under key with its own cancelable
// context; ok is false while another run holds the key.
func beginHealthRun(parent context.Context, key string, timeout time.Duration) (*healthRun, context.Context, bool) {
	healthRunsMu.Lock()
	defer healthRunsMu.Unlock()
	if _, busy := healthRuns[key]; busy {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	run := &healthRun{cancel: cancel}
	healthRuns[key] = run
	return run, ctx, true
}

func endHealthRun(key string, run *healthRun) {
	run.cancel()
	healthRunsMu.Lock()
	defer healthRunsMu.Unlock()
	if healthRuns[key] == run {
		delete(healthRuns, key)
	}
}

// CancelHealthRun stops a running check ("check:<mode>") or fix (its fix
// id, or agent.install:<id>). The CLI child gets SIGTERM, so it can end the
// installers it started, and is killed only if it hasn't exited after
// stopGracefully's grace period. Returns {"ok":true,"cancelled":bool}.
func (a *App) CancelHealthRun(id string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	healthRunsMu.Lock()
	run := healthRuns[runKey(id)]
	healthRunsMu.Unlock()
	if run == nil {
		return `{"ok":true,"cancelled":false}`
	}
	run.cancelled.Store(true)
	run.cancel()
	a.emitLog("HEALTH", "INFO", "cancelled "+id)
	return `{"ok":true,"cancelled":true}`
}

// healthFixEvent is one progress event for the frontend: the CLI's own
// NDJSON line ({"kind":"line"|"done"|"error","message"}) tagged with the fix.
// Cancelled marks the final error of a run the person cancelled.
type healthFixEvent struct {
	FixID     string `json:"fix_id"`
	Kind      string `json:"kind"`
	Message   string `json:"message,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// RunHealthFix applies one fix in the background, relaying progress as
// "health:fixProgress" events, and returns at once. The frontend has
// already asked the user for confirm fixes.
func (a *App) RunHealthFix(fixID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	// One run per fix (per runtime for installs): two `doctor fix`
	// processes for the same fix would start two daemons or two npm
	// installs into one global folder.
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	args, err := healthFixArgs(pid, fixID)
	if err != nil {
		return aiError(err)
	}
	return a.startStreamed(cliBin, fixID, args, fmt.Sprintf("%s is already running", fixID))
}

// stopGracefully makes the context's cancel (Cancel, a timeout, the app
// closing) ask the CLI to stop instead of killing it outright, so it can
// end the installers it started; WaitDelay then kills it, and stops a child
// that still holds the output pipe from blocking the read.
func stopGracefully(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return terminateCLI(cmd) }
	cmd.WaitDelay = healthGracePeriod
}

// healthGracePeriod is how long a cancelled CLI child gets to stop.
var healthGracePeriod = 15 * time.Second

// agentInstallArgs builds `[--profile P] --json agent install [--force]
// [--approve-script URL] -- <id>`. Not --yes: approveURL is the vendor
// script the person was shown and agreed to, and the CLI runs a script only
// if the recipe it scans now names that exact URL. An npm install needs no
// approval flag.
func agentInstallArgs(profileID, runtimeID string, update bool, approveURL string) ([]string, error) {
	args, err := scopedCLIArgs(profileID, "agent", "install")
	if err != nil {
		return nil, err
	}
	if update {
		args = append(args, "--force")
	}
	if approveURL != "" {
		args = append(args, "--approve-script", approveURL)
	}
	return append(args, "--", runtimeID), nil
}

// InstallAgentRuntime installs (update: reinstalls) one AI agent runtime in
// the background, relaying progress as "health:fixProgress" events keyed
// "agent.install:<id>", and returns at once.
func (a *App) InstallAgentRuntime(runtimeID string, update bool, approveURL string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	cliBin, err := findMonoAgentCLI()
	if err != nil {
		return aiError(err)
	}
	pid, err := a.profileID()
	if err != nil {
		return aiError(err)
	}
	args, err := agentInstallArgs(pid, runtimeID, update, approveURL)
	if err != nil {
		return aiError(err)
	}
	return a.startStreamed(cliBin, "agent.install:"+runtimeID, args,
		fmt.Sprintf("%s is already being installed", runtimeID))
}

// refreshesPath reports whether a successful fix can put new programs on
// the managed Node's paths: Node itself, and anything npm installs into its
// prefix (monomind, agent runtimes). The GUI then activates the managed Node
// again, so the children it starts later find them without a restart
// (#137 item 4).
func refreshesPath(fixID string) bool {
	switch fixID {
	case "monomind.node.install", "monomind.node.update", "monomind.install":
		return true
	}
	return strings.HasPrefix(runKey(fixID), "runtime:")
}

// activateNode is nodemgr.Activate (a variable for tests).
var activateNode = nodemgr.Activate

// startStreamed starts a streamed CLI command under its run key and
// returns at once; the final event is sent after a PATH refresh, so the
// re-check the frontend runs on it already sees what was installed.
func (a *App) startStreamed(cliBin, fixID string, args []string, busyMsg string) string {
	key := runKey(fixID)
	run, ctx, ok := beginHealthRun(a.parentCtx(), key, healthFixTimeout)
	if !ok {
		return aiError(errors.New(busyMsg))
	}
	go func() {
		defer endHealthRun(key, run)
		final := a.streamHealthFix(ctx, run, cliBin, fixID, args, a.emitFixEvent)
		if final.Kind == "done" && refreshesPath(fixID) {
			activateNode(a.parentCtx())
		}
		a.emitFixEvent(final)
	}()
	return `{"ok":true}`
}

func (a *App) emitFixEvent(ev healthFixEvent) {
	if a.ctx != nil {
		eventsEmit(a.ctx, "health:fixProgress", ev)
	}
}

// streamHealthFix runs one streamed CLI command (a `doctor fix` or an
// `agent install`, both printing NDJSON progress), relays its progress
// lines through emit and returns its final event (done or error), which
// the caller sends. Output that isn't an event is relayed as a line; a CLI
// that ends without a final event becomes an error with its stderr.
func (a *App) streamHealthFix(ctx context.Context, run *healthRun, cliBin, fixID string, args []string, emit func(healthFixEvent)) healthFixEvent {
	a.emitLog("HEALTH", "INFO", fmt.Sprintf("$ %s %s", cliBin, strings.Join(args, " ")))
	cmd := exec.CommandContext(ctx, cliBin, args...)
	suppressConsole(cmd)
	stopGracefully(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return healthFixEvent{FixID: fixID, Kind: "error", Message: err.Error()}
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return healthFixEvent{FixID: fixID, Kind: "error", Message: err.Error()}
	}
	var final *healthFixEvent
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var ev healthFixEvent
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Kind == "" {
			ev = healthFixEvent{Kind: "line", Message: sc.Text()}
		}
		ev.FixID = fixID
		if ev.Kind == "done" || ev.Kind == "error" {
			final = &ev
			continue
		}
		emit(ev)
	}
	if err := sc.Err(); err != nil {
		// A line too long for the scanner: keep draining so the CLI isn't
		// blocked writing to a full pipe until the timeout.
		emit(healthFixEvent{FixID: fixID, Kind: "line", Message: "(output line too long to show)"})
		_, _ = io.Copy(io.Discard, stdout)
	}
	waitErr := cmd.Wait()
	level := "INFO"
	if waitErr != nil {
		level = "ERROR"
	}
	a.emitLog("HEALTH", level, fmt.Sprintf("%s finished", fixID))
	switch {
	case run != nil && run.cancelled.Load():
		return healthFixEvent{FixID: fixID, Kind: "error", Message: "cancelled", Cancelled: true}
	case final != nil:
		return *final
	}
	// The CLI died without its final event (killed, timed out, crashed).
	msg := "the fix stopped without reporting a result"
	if waitErr != nil {
		msg = waitErr.Error()
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		msg += ": " + s
	}
	return healthFixEvent{FixID: fixID, Kind: "error", Message: msg}
}
