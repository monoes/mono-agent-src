package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/browserdetect"
	"github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/extensioncli"
	"github.com/monoes/mono-agent/internal/cli/integrations"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/logging"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/statedir"
)

// addServiceHooks wires the browser, services and integrations checks to
// the real machine. d carries doctor's --db-path/--profile to a daemon
// it starts.
func addServiceHooks(env *health.Env, d EnvDeps) {
	env.FindBrowser = browserdetect.FindBrowser
	env.ExtensionInstalled = browserdetect.ExtensionInstalled
	env.ExtensionDir = browserdetect.ExtensionDir
	env.Bridge = func(ctx context.Context) (health.BridgeInfo, bool) {
		st, addr, ok := extensioncli.FindRunningBridge(ctx) // read-only GET of /monoagent/health
		if !ok {
			return health.BridgeInfo{}, false
		}
		hb, live := daemonhb.Read()
		daemonPID := 0
		if live {
			daemonPID = hb.PID
		}
		unit := serviceUnit(st.PID)
		return health.BridgeInfo{Addr: extensioncli.BridgeAddr(st, addr), URL: addr, Status: st.Status, PID: st.PID, Version: st.Version, UptimeSec: st.UptimeSec,
			Owner: bridgeOwner(st.PID, daemonPID, processCommand(st.PID), unit)}, true
	}
	env.ClientPairing = func(_ context.Context, baseURL string) string {
		state, _ := extension.CheckPairing(baseURL) // read-only GET of /monoagent/auth
		return string(state)
	}
	// Resolved on demand: `systemctl --user show` is too slow for every health check.
	env.BridgeService = func(ctx context.Context, pid int) string {
		return bridgeUserService(ctx, pid, serviceUnit(pid))
	}
	env.RestartBridge = func(ctx context.Context, unit string, pid int, progress func(string)) error {
		st, _, ok := extensioncli.FindRunningBridge(ctx)
		if !ok || st.PID != pid || unit == "" || bridgeUserService(ctx, pid, serviceUnit(pid)) != unit {
			return fmt.Errorf("the bridge is no longer pid %d run by %s — not restarting", pid, unit)
		}
		progress("restarting " + unit)
		out, err := exec.CommandContext(ctx, "systemctl", "--user", "restart", "--", unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("restarting %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	env.Daemon = func(context.Context) health.DaemonInfo {
		hb, live := daemonhb.Read()
		return health.DaemonInfo{Running: live, PID: hb.PID, APIAddr: hb.APIAddr, BridgeAddr: hb.BridgeAddr, Version: hb.Version, AgeMS: time.Since(hb.TS).Milliseconds()}
	}
	env.APIHealth = func(ctx context.Context, addr string) error {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET /health: HTTP %d", resp.StatusCode)
		}
		return nil
	}
	env.AutostartStatus = autostart.New().Status
	env.InstallAutostart = func(ctx context.Context, progress func(string)) error {
		res, err := autostart.New().Install(ctx)
		if err != nil {
			return err
		}
		progress(res.Description)
		return nil
	}
	env.StartDaemon = func(ctx context.Context, progress func(string)) error {
		// The profile as doctor resolved it: --profile may be a name.
		return startDaemon(ctx, daemonArgs(d.DBPath(), d.ProfileID(), env.ProfileID), autostart.New(), progress)
	}
	env.StopDaemon = stopDaemon
	env.CanStartDaemon = func(ctx context.Context) error {
		return daemonStartBlocked(ctx, daemonArgs(d.DBPath(), d.ProfileID(), env.ProfileID), autostart.New())
	}

	env.ClaudeSkills = integrations.ClaudeSkillsState
	env.InstallClaudeSkills = func() error { return integrations.InstallClaudeSkill(false) }
	// Every installed coding-agent runtime (Claude Code, Codex, Gemini CLI,
	// opencode), registered with --allow-mutations (internal/cli/integrations).
	env.MCPRegistration = integrations.AgentsMCPRegistration
	env.MCPRuntimes = integrations.AgentsMCPRuntimes
	env.RegisterMCP = integrations.RegisterAgentsMCP
}

// startDaemon starts `monoagentcli daemon` through its login service when
// one is registered (idempotent), otherwise as a detached background
// process logging to ~/.monoagent/logs/daemon.log. The daemon gets
// doctor's --db-path and --profile; the login service runs with the
// defaults, so it is not used when those were changed.
func startDaemon(ctx context.Context, args []string, as autostart.Installer, progress func(string)) error {
	// A daemon that is still starting has no heartbeat yet, but it holds
	// the lock: starting another would be refused by it anyway, and must
	// not be spawned while one comes up (a second click, a re-check).
	if daemonhb.Locked() {
		progress("a daemon is already running or starting")
		return nil
	}
	if ok, where := as.Status(ctx); ok {
		if err := serviceStartRefusal(where, args); err != nil {
			return err
		}
		progress("starting the registered service (" + where + ")")
		return as.Start(ctx)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	logDir := filepath.Join(home, ".monoagent", "logs")
	if err := statedir.MkdirAll(logDir); err != nil {
		return err
	}
	logPath := filepath.Join(logDir, "daemon.log")
	if _, err := logging.RotateDaemonLog(logPath); err != nil {
		progress(fmt.Sprintf("could not rotate %s: %v", logPath, err))
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, statedir.FilePerm)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(exe, args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd, err = monomind.StartDetached(cmd)
	if err != nil {
		return fmt.Errorf("starting the daemon: %w", err)
	}
	progress(fmt.Sprintf("started `%s %s` in the background (pid %d, log %s)", filepath.Base(exe), strings.Join(args, " "), cmd.Process.Pid, logPath))
	return cmd.Process.Release()
}

// serviceStartRefusal is why the registered login service, at where, cannot
// start the daemon with args: nil when it can. The service runs the daemon
// with the default database and profile, and one daemon runs per home (the
// service's would take the lock), so it cannot start one with others.
func serviceStartRefusal(where string, args []string) error {
	if len(args) <= 1 {
		return nil
	}
	return fmt.Errorf("the registered login service (%s) runs the daemon with the default database and profile, not %s — "+
		"start it yourself: monoagentcli %s", where, strings.Join(args[1:], " "), strings.Join(args, " "))
}

// daemonStartBlocked says, before anything is stopped, whether startDaemon
// would refuse to start the daemon with args (the registered service cannot
// run it with another database or profile): nil when it would not.
func daemonStartBlocked(ctx context.Context, args []string, as autostart.Installer) error {
	if ok, where := as.Status(ctx); ok {
		return serviceStartRefusal(where, args)
	}
	return nil
}

// daemonStopPollInterval is how often stopDaemon re-checks whether the daemon
// has stopped.
const daemonStopPollInterval = 300 * time.Millisecond

// daemonKillWait is how long stopDaemon waits for the daemon to go after
// SIGKILL before it gives up. A variable so that a test can lengthen it on a
// loaded machine.
var daemonKillWait = 5 * time.Second

// daemonStopGrace is how long stopDaemon waits for a SIGTERM'd daemon to
// finish its own graceful shutdown (draining in-flight workflow executions,
// see daemon.go) before it escalates to a forced kill. A variable so that a
// test can shorten it.
var daemonStopGrace = 15 * time.Second

// stopDaemon stops the daemon at pid so a caller can start a fresh one on
// the current binary without racing its own shutdown: SIGTERM, then wait for
// it to exit, escalating to SIGKILL if it has not gone within daemonStopGrace.
//
// It waits for the process and not for the daemon lock alone. A service
// manager that keeps the daemon alive (launchd's KeepAlive) starts a new one
// the instant the old one exits, and that one may hold the lock before the
// next poll sees it free, so a wait for the lock could outlast the old daemon
// and then fail to kill a process that is gone. A process that has exited but
// that its parent has not reaped (a daemon this process started) still answers
// kill -0, and its lock is free: when the lock was held at the start, a free
// lock counts as stopped too. A daemon built before the lock existed holds
// none, and only its exit tells.
func stopDaemon(ctx context.Context, pid int, progress func(string)) error {
	if pid <= 1 {
		// kill(0), kill(-1) and kill(1) are a process group, every process and init.
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	if !daemonhb.ProcessAlive(pid) {
		return nil // already stopped
	}
	if !heartbeatNames(pid) {
		return fmt.Errorf("pid %d is not the daemon this machine's heartbeat names — not signaling it", pid)
	}
	holdsLock := daemonhb.Locked()
	stopped := func() bool {
		return !daemonhb.ProcessAlive(pid) || (holdsLock && !daemonhb.Locked())
	}
	if err := chat.TerminateProcess(pid); err != nil && !stopped() {
		return fmt.Errorf("signaling pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(daemonStopGrace)
	forced := false
	for !stopped() {
		if time.Now().After(deadline) {
			if forced {
				return fmt.Errorf("pid %d would not stop even after SIGKILL", pid)
			}
			// No second heartbeatNames check here: the daemon removes its heartbeat
			// as soon as SIGTERM arrives, so it would always fail while a hung
			// daemon drains. pid was verified before the signal went out, and the
			// loop saw it running as recently as one poll ago.
			progress(fmt.Sprintf("pid %d did not stop within %s — forcing it", pid, daemonStopGrace))
			if err := chat.KillProcess(pid); err != nil && !stopped() {
				return fmt.Errorf("force-stopping pid %d: %w", pid, err)
			}
			forced = true
			deadline = time.Now().Add(daemonKillWait)
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(daemonStopPollInterval):
		}
	}
	progress(fmt.Sprintf("stopped pid %d", pid))
	return nil
}

// heartbeatNames reports whether the daemon's heartbeat names pid and is live
// (fresh, and its process running). It is what lets stopDaemon refuse a pid
// that is not the daemon this machine runs: the bridge's pid comes from
// whatever answered on its port, and the heartbeat is what the daemon itself
// wrote. It does not say who holds the lock: the file holds a pid and a time,
// and a crash leaves it behind until it goes stale.
func heartbeatNames(pid int) bool {
	hb, ok := daemonhb.Read()
	return ok && hb.PID == pid
}

// daemonArgs is `daemon` plus the --db-path (dbPath) and --profile (profileFlag) doctor runs
// with, when they were given: profileID is the profile as doctor resolved it (--profile may be
// a name).
func daemonArgs(dbPath, profileFlag, profileID string) []string {
	args := []string{"daemon"}
	if dbPath != "" && clicore.ExpandPath(dbPath) != clicore.ExpandPath(capturecli.DefaultDBPath) {
		args = append(args, "--db-path", clicore.ExpandPath(dbPath))
	}
	if profileFlag != "" && profileID != "" && profileID != "default" && strings.ToLower(profileFlag) != "default" {
		args = append(args, "--profile", profileID)
	}
	return args
}

// bridgeOwner says what runs the bridge process pid: the daemon (pid
// daemonPID), an `extension serve`, under a systemd/launchd service or by
// hand. command is its command line and unit its systemd unit ("" when
// unknown or none).
func bridgeOwner(pid, daemonPID int, command, unit string) string {
	if pid <= 0 {
		return ""
	}
	var what string
	switch {
	case pid == daemonPID:
		what = fmt.Sprintf("the daemon (pid %d)", pid)
	case strings.Contains(command, "extension serve"):
		what = fmt.Sprintf("`monoagentcli extension serve` (pid %d)", pid)
	case strings.Contains(command, " daemon"):
		what = fmt.Sprintf("a daemon that is not this home's (pid %d: %s)", pid, command)
	case command != "":
		what = fmt.Sprintf("pid %d: %s", pid, command)
	default:
		return fmt.Sprintf("pid %d", pid)
	}
	switch {
	case unit != "":
		what += ", systemd service " + unit
	case pid != daemonPID && command != "" && runtime.GOOS == "linux":
		what += ", started by hand"
	}
	return what
}

// processCommand is pid's command line (clicore.ProcessCommand).
func processCommand(pid int) string { return clicore.ProcessCommand(pid) }

// serviceUnit is the systemd service pid runs under (Linux), from its
// cgroup: the last path element when it is a "*.service" other than the
// user manager itself. "" otherwise (a terminal's scope, not Linux).
func serviceUnit(pid int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return ""
	}
	return unitFromCgroup(string(b))
}

// A cgroup may name a system service, or a child of a user service. Only
// restart through the user manager when the unit's MainPID is the bridge
// itself, or (a wrapper shell is MainPID) the bridge sits inside the unit's
// own cgroup, which must belong to the user manager.
func bridgeUserService(ctx context.Context, pid int, unit string) string {
	if runtime.GOOS != "linux" || pid <= 0 || unit == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=MainPID", "--value", "--", unit).Output()
	if err != nil {
		return ""
	}
	if strings.TrimSpace(string(out)) == strconv.Itoa(pid) {
		return unit
	}
	out, err = exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ControlGroup", "--value", "--", unit).Output()
	if err != nil {
		return ""
	}
	group := strings.TrimSpace(string(out))
	if !strings.Contains(group, "/user@") || filepath.Base(group) != unit {
		return ""
	}
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/cgroup", procRoot, pid))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[2] == group {
			if bridgeBelongsToUnit(ctx, pid, unit) {
				return unit
			}
			return ""
		}
	}
	return ""
}

// bridgeBelongsToUnit is true when the unit's MainPID is an ancestor of the
// bridge, or the unit's ExecStart references the bridge's command. Sitting in
// the unit's cgroup alone is not enough: a hand-started bridge can land in
// another service's cgroup (e.g. a terminal server's).
func bridgeBelongsToUnit(ctx context.Context, pid int, unit string) bool {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=MainPID", "--value", "--", unit).Output()
	if err != nil {
		return false
	}
	if main, err := strconv.Atoi(strings.TrimSpace(string(out))); err == nil && main > 0 && isAncestor(main, pid) {
		return true
	}
	out, err = exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ExecStart", "--value", "--", unit).Output()
	if err != nil {
		return false
	}
	execStart := string(out)
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/cmdline", procRoot, pid))
	if err != nil {
		return false
	}
	args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	if len(args) > 2 {
		args = args[:2]
	}
	for _, a := range args {
		if a == "" || !strings.Contains(execStart, a) {
			return false
		}
	}
	return true
}

// isAncestor reports whether anc is a (transitive) parent of pid, walking
// PPid through procRoot.
func isAncestor(anc, pid int) bool {
	for i := 0; i < 64 && pid > 1; i++ {
		b, err := os.ReadFile(fmt.Sprintf("%s/%d/status", procRoot, pid))
		if err != nil {
			return false
		}
		ppid := 0
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "PPid:"); ok {
				ppid, _ = strconv.Atoi(strings.TrimSpace(v))
				break
			}
		}
		if ppid == anc {
			return true
		}
		pid = ppid
	}
	return false
}

// procRoot is where /proc lives; tests point it at a temp dir.
var procRoot = "/proc"

func unitFromCgroup(cgroup string) string {
	for _, line := range strings.Split(strings.TrimSpace(cgroup), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		last := filepath.Base(parts[2])
		if strings.HasSuffix(last, ".service") && !strings.HasPrefix(last, "user@") {
			return last
		}
	}
	return ""
}
