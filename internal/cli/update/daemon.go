package update

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/health"
)

// A running daemon keeps the code it started with, so an update leaves the process that runs the
// schedules on the old version until it restarts. `update` and `update --app` therefore restart a
// registered daemon, unless an execution is in flight: an update never interrupts a run. The
// restart goes through the service manager in this process, not through the new binary's
// `daemon restart`, which is a gated command the new binary may refuse.

// DaemonUpdate is what an update did about the daemon, in `update --app --json` as "daemon".
type DaemonUpdate struct {
	// Action is restarted, busy (an execution is in flight), settings (the saved API settings cannot
	// be used), unknown (the database could not be read), not_registered, not_managed (the registered
	// service is not this daemon, or cannot be told to be) or failed.
	Action   string `json:"action"`
	Running  string `json:"running_version,omitempty"` // the version the daemon runs, "" when its heartbeat names none
	InFlight int    `json:"in_flight,omitempty"`       // the executions that kept it from restarting
	Via      string `json:"via,omitempty"`             // the service manager that restarted it
	// Reason says why a failed restart failed, when it is one a rollback answers: stopped (the service manager stopped it and could not
	// start it again), timed_out (the service manager did not answer in time) or no_heartbeat (it restarted and no live heartbeat of the new version followed).
	Reason string `json:"reason,omitempty"`
	// RolledBack: the binary the service starts was put back to the previous one because of that failure (see settleReplaced).
	RolledBack bool   `json:"rolled_back,omitempty"`
	Message    string `json:"message"`
}

// restartBound is the longest a service manager is given to restart the daemon; a variable so a test can shorten it.
var restartBound = 30 * time.Second

// How the restart is verified by default: the daemon must write a live heartbeat of the new version.
const (
	restartVerifyWait = 15 * time.Second
	restartVerifyTick = 250 * time.Millisecond
)

const daemonDown = "daemon is down — start it with: monoagentcli daemon start"

// daemonAfterUpdate restarts the running daemon onto newVersion, or says why not. nil means there
// was nothing to do: no daemon runs, or it already runs newVersion. Only the daemon of this home's
// heartbeat is considered, and only through the service manager it is registered with.
func (d Deps) daemonAfterUpdate(ctx context.Context, newVersion string) *DaemonUpdate {
	if ctx == nil {
		ctx = context.Background()
	}
	hb, live := d.heartbeat()
	if !live || !health.DaemonVersionStale(hb.Version, newVersion) {
		return nil
	}
	was := hb.Version
	if was == "" {
		was = "an older version"
	}
	u := &DaemonUpdate{Running: hb.Version}
	notRestarted := fmt.Sprintf("The daemon (pid %d) runs %s and was not restarted: ", hb.PID, was)
	const keeps = " Run `monoagentcli daemon restart` when that is settled; until then it keeps that version."

	n, settingsErr, err := d.RestartBlockers(ctx, hb.PID)
	switch {
	case err != nil:
		u.Action, u.Message = "unknown", notRestarted+fmt.Sprintf("it could not be told whether a run is in flight (%v).", err)+keeps
		return u
	case n > 0:
		u.Action, u.InFlight = "busy", n
		u.Message = notRestarted + fmt.Sprintf("%d workflow execution(s) are running in it, and an update never interrupts a run.", n) + keeps
		return u
	case settingsErr != nil:
		u.Action = "settings"
		u.Message = notRestarted + fmt.Sprintf("the settings saved with `monoagentcli api config` cannot be used, and a daemon that cannot use them starts without the OpenAI-compatible API (%v).", settingsErr) + keeps
		return u
	}

	// The service is per user, not per home: restart it only when it runs the daemon of this home's
	// heartbeat, never a hand-started one of a scratch or other profile home.
	in := d.Installer()
	if registered, _ := in.Status(ctx); registered {
		pid, perr := d.servicePID(ctx, in)
		if perr != nil || pid != hb.PID {
			u.Action = "not_managed"
			why := fmt.Sprintf("the registered service runs pid %d, not this daemon", pid)
			if perr != nil {
				why = fmt.Sprintf("it could not be told whether the registered service runs it (%v)", perr)
			}
			u.Message = fmt.Sprintf("The daemon (pid %d) runs %s and was not restarted: %s. Restart it manually: stop it and start `monoagentcli daemon` again.", hb.PID, was, why)
			return u
		}
	}

	rctx, cancel := context.WithTimeout(ctx, restartBound)
	defer cancel()
	res, err := autostart.RestartRegistered(rctx, in)
	var notRegistered *autostart.NotRegisteredError
	var stopped *autostart.StoppedError
	switch {
	case errors.As(err, &notRegistered):
		u.Action = "not_registered"
		u.Message = fmt.Sprintf("The daemon (pid %d) runs %s and is not registered for auto-start, so nothing can restart it: stop it and start `monoagentcli daemon` again, or run `monoagentcli daemon install` to have the system manage it.", hb.PID, was)
	case errors.As(err, &stopped):
		u.Action, u.Reason, u.Message = "failed", "stopped", fmt.Sprintf("The daemon (pid %d) was stopped but the service manager could not start it again (%v): %s.", hb.PID, err, daemonDown)
	case errors.Is(err, context.DeadlineExceeded):
		u.Action, u.Reason = "failed", "timed_out"
		u.Message = fmt.Sprintf("The service manager did not restart the daemon (pid %d) within %s: %s.", hb.PID, restartBound, daemonDown)
	case err != nil:
		u.Action, u.Message = "failed", notRestarted+fmt.Sprintf("the service manager failed (%v).", err)+keeps
	case !d.waitForVersion(ctx, newVersion):
		u.Action, u.Via, u.Reason = "failed", res.Via, "no_heartbeat"
		u.Message = fmt.Sprintf("Restarted the daemon through %s but no live heartbeat of %s followed within %s: %s.", res.Via, newVersion, d.verifyWait(), daemonDown)
	default:
		u.Action, u.Via = "restarted", res.Via
		u.Message = fmt.Sprintf("Restarted the daemon through %s so that it runs %s (it ran %s).", res.Via, newVersion, was)
	}
	return u
}

// waitForVersion polls the heartbeat until a live daemon runs newVersion, or the wait ends.
func (d Deps) waitForVersion(ctx context.Context, newVersion string) bool {
	wait, tick := d.verifyWait(), d.VerifyTick
	if tick == 0 {
		tick = restartVerifyTick
	}
	deadline := time.Now().Add(wait)
	for {
		if hb, live := d.heartbeat(); live && !health.DaemonVersionStale(hb.Version, newVersion) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(tick):
		}
	}
}

func (d Deps) heartbeat() (daemonhb.Heartbeat, bool) {
	if d.Heartbeat != nil {
		return d.Heartbeat()
	}
	return daemonhb.Read()
}

func (d Deps) servicePID(ctx context.Context, in autostart.Installer) (int, error) {
	if d.ServicePID != nil {
		return d.ServicePID(ctx, in)
	}
	return autostart.ServiceMainPID(ctx, in)
}

func (d Deps) verifyWait() time.Duration {
	if d.VerifyWait != 0 {
		return d.VerifyWait
	}
	return restartVerifyWait
}

// waitForLive polls the heartbeat until a live daemon of any version has written one after since (so not the one of the
// daemon that was stopped), or the wait ends.
func (d Deps) waitForLive(ctx context.Context, since time.Time) bool {
	wait, tick := d.verifyWait(), d.VerifyTick
	if tick == 0 {
		tick = restartVerifyTick
	}
	deadline := time.Now().Add(wait)
	for {
		if hb, live := d.heartbeat(); live && hb.TS.After(since) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(tick):
		}
	}
}
