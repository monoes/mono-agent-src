package update

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/health"
)

// The extension bridge keeps the code it started with, like the daemon. After an update it is restarted onto the new binary
// when it is the user's own service, no command is in flight on it, and a restart can be verified; otherwise the operator is
// told, with the exact command. A bridge that is not a user service of this user is never touched.

// bridgeRestartBound is the longest the service manager is given to restart the bridge: longer than systemd's own stop timeout
// (90 s), so a restart that is only slow to stop is not reported as failed while it completes; a variable so a test can shorten it.
var bridgeRestartBound = 100 * time.Second

// BridgeState is the extension bridge that answers on this machine, as the update needs to know it.
type BridgeState struct {
	PID      int
	Version  string
	InFlight int    // commands dispatched to the extension and not yet answered
	Unit     string // the user service that owns it, verified; "" when it is not known to be one
	// Why says why Unit is empty: not_a_service (started by hand), unverified (the user manager could not confirm the service
	// the process runs in) or unsupported (this system has no user services to ask). ServiceName is the unit it seems to run in.
	Why         string
	ServiceName string
	// DaemonOwned: it is the daemon's own bridge; the daemon restart restarts it.
	DaemonOwned bool
}

// BridgeUpdate is what an update did about the bridge, in `update --app --json` as "bridge".
type BridgeUpdate struct {
	// Action is restarted, busy (a command is in flight on it), not_managed (it is not known to be one of the user's services), skipped (the caller did not restart it now) or failed.
	Action  string `json:"action"`
	Running string `json:"running_version,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Message string `json:"message"`
}

// bridgeSkipReason is why the bridge is not restarted yet, given the daemon's outcome: "" when it may be.
func bridgeSkipReason(du *DaemonUpdate) string {
	if du != nil && du.Action == "failed" {
		return "the daemon update did not settle (see above), so the new binary is in doubt."
	}
	return ""
}

// restartCommand is the command that restarts a bridge unit, as the operator pastes it: the name quoted and after `--`, so a
// name that starts with a dash is not read as an option.
func restartCommand(unit string) string { return "systemctl --user restart -- " + shQuote(unit) }

// bridgeAfterUpdate restarts the bridge onto newVersion, or says why not. skipBecause, when set, is the reason the caller has not to
// restart it now (the daemon update did not settle): the bridge is left as it is and the reason is told. nil: there was nothing to do (no bridge answers, it
// already runs newVersion, or it is the daemon's own and the daemon restart covers it).
func (d Deps) bridgeAfterUpdate(ctx context.Context, newVersion string, skipBecause string) *BridgeUpdate {
	if d.Bridge == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b, ok := d.Bridge(ctx)
	if !ok || !health.DaemonVersionStale(b.Version, newVersion) || b.DaemonOwned {
		return nil
	}
	was := b.Version
	if was == "" {
		was = "an older version"
	}
	u := &BridgeUpdate{Running: b.Version, Unit: b.Unit}
	if skipBecause != "" {
		u.Action = "skipped"
		u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: %s", b.PID, was, skipBecause)
		if b.Unit != "" {
			u.Message += " Restart the bridge when that is resolved: " + restartCommand(b.Unit)
		}
		return u
	}
	switch {
	case b.Unit == "":
		u.Action = "not_managed"
		switch b.Why {
		case "unverified":
			// it looks like a service but could not be confirmed (systemctl --user is not reachable here, or the unit's main
			// process is another one): never tell the operator to start a second bridge
			u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: it seems to run as the service %s but that could not be confirmed from here (systemctl --user is not reachable). Restart it from your own session with: %s", b.PID, was, b.ServiceName, restartCommand(b.ServiceName))
		case "unsupported":
			u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: restarting a bridge service is only done on Linux. Restart it the way you started it.", b.PID, was)
		default:
			u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: it is not run by a service of yours. Stop it and start `monoagentcli extension serve` again.", b.PID, was)
		}
		return u
	case b.InFlight > 0:
		u.Action = "busy"
		u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: %d command(s) are in flight on it, and an update never interrupts them. Restart it when that is settled: %s", b.PID, was, b.InFlight, restartCommand(b.Unit))
		return u
	}
	restart := restartCommand(b.Unit)
	rctx, cancel := context.WithTimeout(ctx, bridgeRestartBound)
	defer cancel()
	if d.RestartBridge == nil {
		u.Action, u.Message = "failed", fmt.Sprintf("The extension bridge (pid %d) runs %s; restart it with: %s", b.PID, was, restart)
		return u
	}
	d.say(fmt.Sprintf("Restarting the extension bridge (%s) onto %s (up to %s)...", b.Unit, newVersion, bridgeRestartBound+d.verifyWait()))
	if err := d.RestartBridge(rctx, b.Unit, b.PID); err != nil {
		u.Action = "failed"
		u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and its restart failed (%v). Restart it with: %s", b.PID, was, err, restart)
		return u
	}
	// the restarted bridge is a new process on the new version, within the wait
	deadline := time.Now().Add(d.verifyWait())
	tick := d.VerifyTick
	if tick == 0 {
		tick = restartVerifyTick
	}
	for {
		if next, ok := d.Bridge(ctx); ok && next.PID != b.PID {
			if !health.DaemonVersionStale(next.Version, newVersion) {
				u.Action = "restarted"
				u.Message = fmt.Sprintf("Restarted the extension bridge (%s) so that it runs %s (it ran %s).", b.Unit, newVersion, was)
				return u
			}
			u.Action = "failed"
			u.Message = fmt.Sprintf("Restarted the extension bridge (%s) but it still runs %s, not %s: the binary %s starts is not the updated one. Check it with: systemctl --user cat %s", b.Unit, next.Version, newVersion, b.Unit, b.Unit)
			return u
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(tick):
		}
	}
	u.Action = "failed"
	u.Message = fmt.Sprintf("Restarted the extension bridge (%s) but no bridge answered within %s. Check it with: systemctl --user status %s", b.Unit, d.verifyWait(), b.Unit)
	return u
}
