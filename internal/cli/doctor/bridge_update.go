package doctor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/extensioncli"
)

// RunningBridge is the extension bridge that answers on this machine, for `update` (which restarts it after the binary it
// runs was replaced). Unit is the systemd user service that owns it, verified, or "" when it is not run by one of the
// user's services.
type RunningBridge struct {
	PID      int
	Version  string
	InFlight int // commands dispatched to the extension and not yet answered
	Unit     string
	// DaemonOwned: the bridge is the daemon's own, so restarting the daemon restarts it.
	DaemonOwned bool
}

// FindBridge asks the bridge that answers (a read-only GET of its health) who it is and who runs it.
func FindBridge(ctx context.Context, daemonPID int) (RunningBridge, bool) {
	st, _, ok := extensioncli.FindRunningBridge(ctx)
	if !ok {
		return RunningBridge{}, false
	}
	return RunningBridge{PID: st.PID, Version: st.Version, InFlight: st.InFlight,
		Unit: bridgeUserService(ctx, st.PID, serviceUnit(st.PID)), DaemonOwned: daemonPID > 0 && st.PID == daemonPID}, true
}

// RestartBridgeUnit restarts unit, refusing unless the bridge is still pid and still owned by unit (the same check
// `doctor fix` makes), so a bridge that is not the user's own service is never restarted.
func RestartBridgeUnit(ctx context.Context, unit string, pid int) error {
	st, _, ok := extensioncli.FindRunningBridge(ctx)
	if !ok || st.PID != pid || unit == "" || bridgeUserService(ctx, pid, serviceUnit(pid)) != unit {
		return fmt.Errorf("the bridge is no longer pid %d run by %s — not restarting", pid, unit)
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "restart", "--", unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("restarting %s: %w: %s", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}
