package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/extensioncli"
)

// RunningBridge is the extension bridge that answers on this machine, for `update` (which restarts it after the binary it
// runs was replaced).
type RunningBridge struct {
	PID      int
	Version  string
	InFlight int // commands dispatched to the extension and not yet answered, at the moment it was asked
	// Unit is the systemd user service that owns it, verified, or "" when it is not known to be one.
	Unit string
	// Why says why Unit is empty: "not_a_service" (on Linux the process is not in a service at all: started by hand),
	// "unverified" (it looks like it runs in a service but the user manager could not confirm it: systemctl --user is not
	// reachable here, or the unit's main process is another one) or "unsupported" (this system has no user services to ask:
	// macOS, Windows). Empty when Unit is set.
	Why string
	// ServiceName is the unit the process runs under as its cgroup names it, when Why is "unverified".
	ServiceName string
	// DaemonOwned: the bridge is the daemon's own, so restarting the daemon restarts it.
	DaemonOwned bool
}

// Seams for the tests: the real ones read the bridge's health endpoint and run systemctl.
var (
	findRunningBridge = extensioncli.FindRunningBridge
	userServiceOf     = bridgeUserService
	cgroupServiceOf   = serviceUnit
	systemctlRestart  = func(ctx context.Context, unit string) ([]byte, error) {
		return exec.CommandContext(ctx, "systemctl", bridgeRestartArgs(unit)...).CombinedOutput()
	}
)

// bridgeRestartArgs is the whole command line after systemctl: the user manager, and "--" so that a unit name is never read as
// an option.
func bridgeRestartArgs(unit string) []string { return []string{"--user", "restart", "--", unit} }

// FindBridge asks the bridge that answers (a read-only GET of its health) who it is and who runs it.
func FindBridge(ctx context.Context, daemonPID int) (RunningBridge, bool) {
	st, _, ok := findRunningBridge(ctx)
	if !ok {
		return RunningBridge{}, false
	}
	b := RunningBridge{PID: st.PID, Version: st.Version, InFlight: st.InFlight, DaemonOwned: daemonPID > 0 && st.PID == daemonPID}
	named := cgroupServiceOf(st.PID)
	switch {
	case runtime.GOOS != "linux":
		b.Why = "unsupported"
	case named == "":
		b.Why = "not_a_service"
	default:
		if b.Unit = userServiceOf(ctx, st.PID, named); b.Unit == "" {
			b.Why, b.ServiceName = "unverified", named
		}
	}
	return b, true
}

// RestartBridgeUnit restarts unit, refusing unless the bridge is still pid, still owned by unit and idle (no command in
// flight), so a bridge that is not the user's own service is never restarted, nor one that picked up work since it was looked
// at. progress, when set, is told once the checks pass and the restart is about to run.
func RestartBridgeUnit(ctx context.Context, unit string, pid int, progress func(string)) error {
	st, _, ok := findRunningBridge(ctx)
	if !ok || st.PID != pid || unit == "" || userServiceOf(ctx, pid, cgroupServiceOf(pid)) != unit {
		return fmt.Errorf("the bridge is no longer pid %d run by %s — not restarting", pid, unit)
	}
	if st.InFlight > 0 {
		return fmt.Errorf("%d command(s) are in flight on the bridge — not restarting", st.InFlight)
	}
	if progress != nil {
		progress("restarting " + unit)
	}
	out, err := systemctlRestart(ctx, unit)
	if err != nil {
		return fmt.Errorf("restarting %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}
