package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// GroupBrowser covers the browser, the MonoAgent extension and its bridge.
const GroupBrowser = "browser"

const (
	CheckBrowser   = "browser.installed"
	CheckExtension = "browser.extension"
	CheckBridge    = "browser.bridge"
	CheckPaired    = "browser.paired"

	FixBrowserInstall       = "browser.install"
	FixExtensionInstall     = "browser.extension.install"
	FixExtensionPair        = "browser.extension.pair"
	FixExtensionPermission  = "browser.extension.permission"
	FixBridgeRestart        = "browser.bridge.restart"
	FixBridgeServiceRestart = "browser.bridge.service.restart"
)

// ClientPairing's answers (extension.PairingState's values).
const (
	ClientPairingOK       = "ok"
	ClientPairingMismatch = "mismatch"
)

var browserFeatures = []string{"crawling", "page capture", "platform logins"}

func browserChecks() []Check {
	return []Check{
		{ID: CheckBrowser, Group: GroupBrowser, Title: "Browser", Features: browserFeatures, Run: checkBrowser},
		{ID: CheckExtension, Group: GroupBrowser, Title: "MonoAgent extension", Features: browserFeatures,
			DependsOn: []string{CheckBrowser}, Run: checkExtension},
		{ID: CheckBridge, Group: GroupBrowser, Title: "Extension bridge", Features: browserFeatures, Run: checkBridge},
		{ID: CheckPaired, Group: GroupBrowser, Title: "Extension connection", Features: browserFeatures,
			DependsOn: []string{CheckBridge}, Run: checkPaired},
	}
}

func browserFixes() []Fix {
	manual := func(context.Context, *Env, func(string)) error { return fmt.Errorf("this needs to be done by hand") }
	return []Fix{
		{FixInfo: FixInfo{ID: FixBridgeServiceRestart, Label: "Restart the extension bridge service", Safety: SafetyConfirm,
			Command: "monoagentcli doctor fix " + FixBridgeServiceRestart}, Apply: fixBridgeServiceRestart},
		{FixInfo: FixInfo{ID: FixBrowserInstall, Label: "Install a Chromium browser", Safety: SafetyManual,
			Command: "install Google Chrome, Microsoft Edge, Chromium or Brave"}, Apply: manual},
		{FixInfo: FixInfo{ID: FixExtensionInstall, Label: "Load the MonoAgent extension", Safety: SafetyManual,
			Command: "open chrome://extensions → enable Developer mode → Load unpacked → the chrome-extension folder"}, Apply: manual},
		{FixInfo: FixInfo{ID: FixExtensionPair, Label: "Pair the extension", Safety: SafetyManual,
			Command: "monoagentcli extension pair --open"}, Apply: manual},
		{FixInfo: FixInfo{ID: FixExtensionPermission, Label: "Grant Full Disk Access", Safety: SafetyManual,
			Command: "System Settings → Privacy & Security → Full Disk Access → enable your terminal app, then re-run doctor"}, Apply: manual},
		{FixInfo: FixInfo{ID: FixBridgeRestart, Label: "Restart the daemon (it runs the extension bridge)", Safety: SafetyConfirm,
			Command: "monoagentcli doctor fix " + FixBridgeRestart}, Apply: fixBridgeRestart},
	}
}

func checkBrowser(_ context.Context, env *Env) Result {
	if env.FindBrowser == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	if p := env.FindBrowser(); p != "" {
		return Result{Status: StatusOK, Summary: p}
	}
	return Result{Status: StatusWarn, Summary: "no Chrome, Edge, Chromium or Brave found", FixID: FixBrowserInstall}
}

func checkExtension(ctx context.Context, env *Env) Result {
	// A connected extension is stronger proof than any filesystem scan below
	// — check it first so a false negative from ExtensionInstalled (its
	// profile directory is unreadable; see the checked flag) never
	// contradicts what the bridge is watching live.
	if env.Bridge != nil {
		if b, ok := env.Bridge(ctx); ok && b.Status == "connected" {
			return Result{Status: StatusOK, Summary: "connected (" + b.Addr + ")"}
		}
	}
	if env.ExtensionInstalled == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	found, checked := env.ExtensionInstalled()
	if found {
		return Result{Status: StatusOK, Summary: "installed in a browser profile"}
	}
	if !checked {
		return Result{Status: StatusWarn, Summary: "could not check — this OS restricts reading browser profiles",
			Detail: "grant Full Disk Access to your terminal, or ignore this once the extension connects",
			FixID:  FixExtensionPermission}
	}
	res := Result{Status: StatusWarn, Summary: "not found in any browser profile", FixID: FixExtensionInstall}
	if env.ExtensionDir != nil {
		if dir := env.ExtensionDir(); filepath.IsAbs(dir) {
			res.Detail = "load unpacked from: " + dir
		} else {
			res.Detail = "load unpacked from the chrome-extension folder of the mono-agent download"
		}
	}
	return res
}

func checkBridge(ctx context.Context, env *Env) Result {
	if env.Bridge == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	b, ok := env.Bridge(ctx)
	if !ok {
		return bridgeDown(ctx, env)
	}
	summary := fmt.Sprintf("%s (pid %d, up %s)", b.Addr, b.PID, time.Duration(b.UptimeSec)*time.Second)
	if b.Owner != "" {
		summary += " — run by " + b.Owner
	}
	if skewed(b.Version, env.Version) {
		res := Result{Status: StatusWarn, Summary: summary,
			Detail: fmt.Sprintf("the bridge runs %s but this CLI is %s — restart whatever started it to pick up the new build", b.Version, env.Version),
			FixID:  FixBridgeRestart}
		// Where no fix is offered, what to do goes in the detail: a result's FixCommand is
		// shown only with a fix, and is not in the report without one.
		unit := ""
		if env.RestartBridge != nil && !isDaemonOwned(ctx, env, b) {
			unit = bridgeServiceUnit(ctx, env, b)
		}
		if unit != "" && !bridgeRestartHelps(b.PID) {
			// The service's binary is the one already running: a restart
			// brings back the same version (it would only interrupt the
			// browser), so none is offered.
			res.FixID = ""
			res.Detail = fmt.Sprintf("the bridge runs %s but this CLI is %s — the %s service already runs its installed binary, so restarting it changes nothing; update one of the two binaries to match", b.Version, env.Version, unit)
		} else if unit != "" {
			res.FixID = FixBridgeServiceRestart
			res.Detail = fmt.Sprintf("the bridge runs %s but this CLI is %s — restart its service: systemctl --user restart %s", b.Version, env.Version, unit)
			res.FixCommand = "systemctl --user restart " + unit
		} else if runtime.GOOS == "windows" {
			// Windows has no way to signal the daemon, so doctor can't restart it.
			res.FixID = ""
			res.Detail += "; doctor can't stop the daemon on Windows, so restart it yourself"
		} else if isDaemonOwned(ctx, env, b) {
			if blocked := startBlocker(ctx, env); blocked != nil {
				// A restart that is certain to be refused is not offered: "Fix issues"
				// would fail every time. The user gets what to do instead.
				res.FixID = ""
				res.Detail += fmt.Sprintf("; doctor can't restart it: %v (stop the daemon, pid %d, first: it holds the daemon lock)", blocked, b.PID)
			} else {
				res.FixCommand = "monoagentcli doctor fix " + FixBridgeRestart
			}
		} else {
			owner := b.Owner
			if owner == "" {
				owner = "whatever started it"
			}
			res.FixCommand = "restart it yourself — it's run by " + owner + ", not this machine's daemon, so doctor can't restart it for you"
		}
		return res
	}
	return Result{Status: StatusOK, Summary: summary}
}

// startBlocker says why a new daemon could not be started once the running
// one is stopped: nil when it could, or when nothing says that it could not.
func startBlocker(ctx context.Context, env *Env) error {
	if env.CanStartDaemon == nil {
		return nil
	}
	return env.CanStartDaemon(ctx)
}

// isDaemonOwned reports whether b is the bridge this machine's own daemon
// serves, as opposed to a bare `extension serve` or someone else's process —
// the one case fixBridgeRestart can safely restart on its own.
func isDaemonOwned(ctx context.Context, env *Env, b BridgeInfo) bool {
	if env.Daemon == nil {
		return false
	}
	d := env.Daemon(ctx)
	return d.Running && d.PID == b.PID
}

// fixBridgeRestart restarts a stale, daemon-owned bridge so it picks up the
// routing (and everything else) the running CLI already has: stop the old
// daemon, then start a fresh one the same way fixDaemonStart does. A bridge
// owned by anything else (a bare `extension serve`, someone's own service)
// is not this fix's to touch — it returns the same instruction checkBridge
// already showed instead of guessing at how to reach that process.
func fixBridgeRestart(ctx context.Context, env *Env, progress func(string)) error {
	if env.Bridge == nil {
		return fmt.Errorf("restarting the bridge is not available here")
	}
	b, ok := env.Bridge(ctx)
	if !ok {
		return fmt.Errorf("no bridge is running to restart")
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("doctor can't stop the daemon on Windows — restart it yourself")
	}
	if !isDaemonOwned(ctx, env, b) {
		owner := b.Owner
		if owner == "" {
			owner = "whatever started it"
		}
		return fmt.Errorf("the bridge on %s is run by %s, not this machine's daemon — restart that yourself "+
			"(e.g. Ctrl+C the terminal running `extension serve`, then run it again)", b.Addr, owner)
	}
	if env.StopDaemon == nil || env.StartDaemon == nil || env.Daemon == nil {
		return fmt.Errorf("restarting the daemon is not available here")
	}
	// Stopping the daemon is the point of no return: when a new one could not be
	// started (the login service refuses a start with another database or
	// profile), refuse first, with the old daemon still running.
	if err := startBlocker(ctx, env); err != nil {
		// The reason ends with "start it yourself": with the daemon still running that
		// only meets its lock, so it is said to be stopped first.
		return fmt.Errorf("not restarting the daemon, which is left running: %w (it holds the daemon lock: stop it, pid %d, before you do)", err, b.PID)
	}
	progress(fmt.Sprintf("stopping the daemon (pid %d, bridge %s)", b.PID, b.Version))
	if err := env.StopDaemon(ctx, b.PID, progress); err != nil {
		return fmt.Errorf("stopping the old daemon: %w", err)
	}
	if err := fixDaemonStart(ctx, env, progress); err != nil {
		return fmt.Errorf("the old daemon is stopped, but a new one did not start: %w", err)
	}
	return nil
}

// bridgeServiceUnit is the user service that owns b, resolved only when asked
// (a skewed version) because it costs a systemctl call.
func bridgeServiceUnit(ctx context.Context, env *Env, b BridgeInfo) string {
	if b.ServiceUnit != "" || env.BridgeService == nil {
		return b.ServiceUnit
	}
	return env.BridgeService(ctx, b.PID)
}

// Re-resolve ownership when clicked: the bridge may have changed since the
// Settings check. The machine hook only restarts a verified owning service.
func fixBridgeServiceRestart(ctx context.Context, env *Env, progress func(string)) error {
	if env.RestartBridge == nil || env.Bridge == nil {
		return fmt.Errorf("restarting the bridge service is not available here")
	}
	b, ok := env.Bridge(ctx)
	unit := ""
	if ok {
		unit = bridgeServiceUnit(ctx, env, b)
	}
	if unit == "" {
		return fmt.Errorf("the bridge is not owned by a restartable user service — restart whatever started it")
	}
	if !skewed(b.Version, env.Version) {
		progress("the bridge already runs the current build")
		return nil
	}
	if err := env.RestartBridge(ctx, unit, b.PID, progress); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if next, ok := env.Bridge(ctx); ok && next.PID != b.PID {
			if skewed(next.Version, env.Version) {
				return fmt.Errorf("the restarted bridge runs %s but this CLI is %s — update the monoagentcli binary used by %s", next.Version, env.Version, unit)
			}
			progress(fmt.Sprintf("bridge running (pid %d, %s)", next.PID, next.Version))
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the restarted bridge: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// bridgeDown reports a bridge that isn't running. Starting the daemon
// brings it up only when no daemon runs yet: a running one either has it
// turned off (--bridge=false) or failed to open it, and starting "the
// daemon" again would do nothing.
func bridgeDown(ctx context.Context, env *Env) Result {
	res := Result{Status: StatusWarn, Summary: "not running"}
	if env.Daemon != nil {
		if d := env.Daemon(ctx); d.Running {
			if d.BridgeAddr == "" {
				res.Summary = fmt.Sprintf("not running — the daemon (pid %d) runs without it", d.PID)
				res.Detail = "it was started with --bridge=false, or its bridge failed to start (see " + daemonLogHint(ctx, env) + "); " +
					"restart the daemon without --bridge=false, or run: monoagentcli extension serve"
			} else {
				res.Summary = fmt.Sprintf("not answering — the daemon (pid %d) should serve it on %s", d.PID, d.BridgeAddr)
				res.Detail = "restart the daemon, or run: monoagentcli extension serve"
			}
			return res
		}
	}
	res.Detail = "the daemon hosts it (monoagentcli daemon), or run: monoagentcli extension serve"
	res.FixID = FixDaemonStart
	return res
}

// skewed reports a real version difference between two release builds.
func skewed(a, b string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	a, b = norm(a), norm(b)
	if a == "" || b == "" || a == "dev" || b == "dev" || strings.Contains(a, "-g") || strings.Contains(b, "-g") {
		return false
	}
	return a != b
}

func checkPaired(ctx context.Context, env *Env) Result {
	b, ok := env.Bridge(ctx)
	if !ok {
		return Result{Status: StatusSkip, Summary: "bridge not running"}
	}
	// The bridge's status is about the extension; whether it serves THIS app
	// is a separate question, asked with this app's token (`extension status`
	// asks it the same way).
	if env.ClientPairing != nil && b.URL != "" && env.ClientPairing(ctx, b.URL) == ClientPairingMismatch {
		return Result{Status: StatusWarn, Summary: "the bridge doesn't accept this app's pairing token",
			Detail: "the bridge on " + b.Addr + " was started with another token (another HOME, or the token was reset since) — " +
				"restart the bridge from this install, then pair the extension",
			FixID: FixExtensionPair}
	}
	switch b.Status {
	case "connected":
		return Result{Status: StatusOK, Summary: "extension connected"}
	case "unpaired":
		return Result{Status: StatusWarn, Summary: "extension is not paired with this bridge", FixID: FixExtensionPair}
	default:
		return Result{Status: StatusInfo, Summary: "extension not connected right now — the browser may be closed or idle"}
	}
}

// bridgeRestartHelps reports whether restarting the bridge's process would
// start a different build: on Linux, the binary it runs was replaced on disk
// since it started (/proc/<pid>/exe reads "... (deleted)"). Where that cannot
// be told it says true, as before. A variable so tests can set it.
var bridgeRestartHelps = func(pid int) bool {
	if runtime.GOOS != "linux" || pid <= 0 {
		return true
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return true
	}
	return strings.HasSuffix(exe, " (deleted)")
}
