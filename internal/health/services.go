package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
)

// GroupServices covers monoagent's long-running processes.
const GroupServices = "services"

const (
	CheckAutostart = "services.autostart"
	CheckDaemon    = "services.daemon"

	FixAutostart     = "services.autostart.install"
	FixDaemonStart   = "services.daemon.start"
	FixDaemonRestart = "services.daemon.restart"
)

var daemonFeatures = []string{"schedules and triggers", "org automations", "extension bridge"}

func serviceChecks() []Check {
	return []Check{
		{ID: CheckAutostart, Group: GroupServices, Title: "Start at login", Run: checkAutostart},
		{ID: CheckDaemon, Group: GroupServices, Title: "Workflow daemon", Features: daemonFeatures, Run: checkDaemon},
	}
}

func serviceFixes() []Fix {
	return []Fix{
		{FixInfo: FixInfo{ID: FixAutostart, Label: "Start the daemon at login", Safety: SafetyConfirm,
			Command: "monoagentcli daemon install", Optional: true}, Apply: func(ctx context.Context, env *Env, progress func(string)) error {
			if env.InstallAutostart == nil {
				return fmt.Errorf("not available here")
			}
			return env.InstallAutostart(ctx, progress)
		}},
		{FixInfo: FixInfo{ID: FixDaemonStart, Label: "Start the workflow daemon", Safety: SafetyConfirm,
			Command: "monoagentcli daemon (in the background)"}, Apply: fixDaemonStart},
		{FixInfo: FixInfo{ID: FixDaemonRestart, Label: "Restart the workflow daemon on this version", Safety: SafetyConfirm,
			Command: "monoagentcli daemon restart"}, Apply: fixDaemonRestart},
	}
}

func checkAutostart(ctx context.Context, env *Env) Result {
	if env.AutostartStatus == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	ok, where := env.AutostartStatus(ctx)
	if ok {
		return Result{Status: StatusOK, Summary: where}
	}
	// where, when set, says why an entry that exists doesn't count (a
	// unit file that systemd doesn't have enabled, a plist not loaded).
	return Result{Status: StatusInfo, Summary: "the daemon does not start at login", Detail: where, FixID: FixAutostart}
}

func checkDaemon(ctx context.Context, env *Env) Result {
	if env.Daemon == nil {
		return Result{Status: StatusSkip, Summary: "not available"}
	}
	d := env.Daemon(ctx)
	if !d.Running {
		return Result{Status: StatusWarn, Summary: "not running", FixID: FixDaemonStart}
	}
	summary := fmt.Sprintf("pid %d, heartbeat %s ago", d.PID, (time.Duration(d.AgeMS) * time.Millisecond).Round(time.Second))
	if d.SchedulerAgeMS > SchedulerStaleAfter.Milliseconds() {
		age := (time.Duration(d.SchedulerAgeMS) * time.Millisecond).Round(time.Second)
		return Result{Status: StatusWarn, Summary: fmt.Sprintf("%s — but its scheduler last ran %s ago", summary, age),
			Detail: "the heartbeat is written by its own timer, so a daemon whose scheduler loop is stuck still looks alive; " +
				"scheduled workflows are not firing. Restarting interrupts whatever the daemon is running",
			FixID: FixDaemonRestart}
	}
	if d.APIAddr != "" && env.APIHealth != nil {
		if err := env.APIHealth(ctx, d.APIAddr); err != nil {
			return Result{Status: StatusWarn, Summary: summary + " — its HTTP API does not answer", Detail: err.Error()}
		}
		summary += ", API " + d.APIAddr
	}
	if DaemonVersionStale(d.Version, env.Version) {
		running := d.Version
		if running == "" {
			running = "a version from before heartbeats carried one"
		}
		return Result{Status: StatusWarn, Summary: fmt.Sprintf("%s - it runs %s, this binary is %s", summary, running, env.Version),
			Detail: "a running daemon keeps the code it started with: it does not have this version's changes until it restarts. " +
				"Restarting interrupts whatever the daemon is running",
			FixID: FixDaemonRestart}
	}
	return Result{Status: StatusOK, Summary: summary}
}

// DaemonVersionStale reports whether a running daemon predates binaryVersion, the version of the
// binary that asks. A development build on either side has no version to compare, so it is never
// stale; a heartbeat with no version is a daemon from before the field existed, so it is.
func DaemonVersionStale(daemonVersion, binaryVersion string) bool {
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	dev := func(v string) bool { return v == "dev" || strings.Contains(v, "-g") }
	bin, d := norm(binaryVersion), norm(daemonVersion)
	if bin == "" || dev(bin) || dev(d) {
		return false
	}
	return d != bin
}

// fixDaemonRestart stops this home's daemon (StopDaemon refuses a pid its heartbeat does not name,
// so a daemon of another home or profile is never touched) and starts it on the current binary.
func fixDaemonRestart(ctx context.Context, env *Env, progress func(string)) error {
	if env.Daemon == nil || env.StopDaemon == nil || env.StartDaemon == nil {
		return fmt.Errorf("restarting the daemon is not available here")
	}
	if d := env.Daemon(ctx); d.Running {
		if err := env.StopDaemon(ctx, d.PID, progress); err != nil {
			return err
		}
	}
	return fixDaemonStart(ctx, env, progress)
}

// fixDaemonStart starts the daemon unless it already runs: through the
// login service when one is registered (idempotent), else as a detached
// background process. Then waits for its heartbeat.
func fixDaemonStart(ctx context.Context, env *Env, progress func(string)) error {
	if env.Daemon == nil || env.StartDaemon == nil {
		return fmt.Errorf("starting the daemon is not available here")
	}
	if env.Daemon(ctx).Running {
		progress("the daemon is already running")
		return nil
	}
	if err := env.StartDaemon(ctx, progress); err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if d := env.Daemon(ctx); d.Running {
			progress(fmt.Sprintf("daemon running (pid %d)", d.PID))
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("the daemon was started but no heartbeat appeared within 15s — see %s", daemonLogHint(ctx, env))
}

// daemonLogHint says where the daemon logs: where the registered login
// service sends its output (the user journal on Linux) when one is
// registered — startDaemon starts the daemon through it then — else the file
// the detached start writes, ~/.monoagent/logs/daemon.log.
func daemonLogHint(ctx context.Context, env *Env) string {
	if env.AutostartStatus != nil {
		if ok, _ := env.AutostartStatus(ctx); ok {
			return serviceLogHint()
		}
	}
	return fallbackLogPath()
}

// Where the daemon logs, by how it was started; tests replace them.
var (
	serviceLogHint  = autostart.ServiceLogHint
	fallbackLogPath = autostart.FallbackLogPath
)
