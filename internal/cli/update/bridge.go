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

// BridgeState is the extension bridge that answers on this machine, as the update needs to know it.
type BridgeState struct {
	PID      int
	Version  string
	InFlight int    // commands dispatched to the extension and not yet answered
	Unit     string // the user service that owns it, verified; "" when it is not one
	// DaemonOwned: it is the daemon's own bridge; the daemon restart restarts it.
	DaemonOwned bool
}

// BridgeUpdate is what an update did about the bridge, in `update --app --json` as "bridge".
type BridgeUpdate struct {
	// Action is restarted, busy (a command is in flight on it), not_managed (it is not one of the user's services) or failed.
	Action  string `json:"action"`
	Running string `json:"running_version,omitempty"`
	Unit    string `json:"unit,omitempty"`
	Message string `json:"message"`
}

// bridgeAfterUpdate restarts the bridge onto newVersion, or says why not. nil: there was nothing to do (no bridge answers, it
// already runs newVersion, or it is the daemon's own and the daemon restart covers it).
func (d Deps) bridgeAfterUpdate(ctx context.Context, newVersion string) *BridgeUpdate {
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
	switch {
	case b.Unit == "":
		u.Action = "not_managed"
		u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: it is not run by a service of yours. Stop it and start `monoagentcli extension serve` again.", b.PID, was)
		return u
	case b.InFlight > 0:
		u.Action = "busy"
		u.Message = fmt.Sprintf("The extension bridge (pid %d) runs %s and was not restarted: %d command(s) are in flight on it, and an update never interrupts them. Restart it when that is settled: systemctl --user restart %s", b.PID, was, b.InFlight, b.Unit)
		return u
	}
	restart := "systemctl --user restart " + b.Unit
	rctx, cancel := context.WithTimeout(ctx, restartBound)
	defer cancel()
	if d.RestartBridge == nil {
		u.Action, u.Message = "failed", fmt.Sprintf("The extension bridge (pid %d) runs %s; restart it with: %s", b.PID, was, restart)
		return u
	}
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
	var last BridgeState
	for {
		if next, ok := d.Bridge(ctx); ok && next.PID != b.PID {
			last = next
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
	_ = last
	u.Action = "failed"
	u.Message = fmt.Sprintf("Restarted the extension bridge (%s) but no bridge answered within %s. Check it with: systemctl --user status %s", b.Unit, d.verifyWait(), b.Unit)
	return u
}
