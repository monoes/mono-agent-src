package update

import (
	"context"
	"fmt"
	"github.com/monoes/mono-agent/internal/autostart"
	"strings"
	"time"
)

// settleReplaced ends the replacement of the binaries staleCLIs swapped in, once the daemon restart has been tried. The
// previous binary of each was kept; it is dropped (committed) when the new one is not in doubt, and put back when the one the
// daemon service starts was replaced and the restart did not bring the daemon up on the new version, because then the
// service has a binary it cannot run and the old one is the last thing known to work. The put-back passes the same rules as
// the replacement, and a restart of its own, with the same bound. du is the restart's result (nil: no daemon ran); the
// returned value is what to tell the operator. bu is the bridge restart's result: when it failed, no previous binary is dropped
// (bu's message says where each is and how to put it back).
func (d Deps) settleReplaced(ctx context.Context, list []StaleCLI, du *DaemonUpdate, bu *BridgeUpdate) *DaemonUpdate {
	du = d.settleDaemon(ctx, list, du)
	d.settleBridge(list, bu)
	return du
}

// settleDaemon is the first part: the daemon's outcome decides which replaced binaries are put back (the one the daemon service
// starts, when the daemon did not come up) or kept for the operator (the daemon may still be starting, or its restart may still
// be in flight). The others stay pending: the bridge restart that follows may still need them to be put back.
func (d Deps) settleDaemon(ctx context.Context, list []StaleCLI, du *DaemonUpdate) *DaemonUpdate {
	if ctx == nil {
		ctx = context.Background()
	}
	duFailed := du != nil && du.Action == "failed"
	rollBack := duFailed && (du.Reason == "no_heartbeat" || du.Reason == "stopped")
	hold := duFailed && (du.Reason == "still_starting" || du.Reason == "timed_out" || du.Reason == "cancelled")
	for _, s := range list {
		if s.undo == nil || s.undo.done {
			continue
		}
		switch {
		case rollBack && s.runsDaemon:
			note, up := d.rollBackOne(ctx, s, du.pid)
			du.recovered = du.recovered || up
			du.RolledBack = du.RolledBack || strings.HasPrefix(note, "Rolled back")
			du.Message += " " + note
			s.undo.done = true
		case hold:
			du.Message += fmt.Sprintf(" The previous binary of %q is kept at %q; put it back with: mv -f %s %s", s.Path, s.undo.bak, shQuote(s.undo.bak), shQuote(s.undo.target))
			s.undo.done = true
		}
	}
	return du
}

// settleBridge is the last part, after the bridge restart: what is still pending is dropped, unless the bridge did not come up on
// the new binary or the restart was skipped because the daemon's outcome is in doubt, when which binary it starts is not known
// here and each previous one is kept (bu's message says where).
func (d Deps) settleBridge(list []StaleCLI, bu *BridgeUpdate) {
	for _, s := range list {
		if s.undo == nil || s.undo.done {
			continue
		}
		if bu != nil && (bu.Action == "failed" || bu.Action == "skipped") {
			bu.Message += fmt.Sprintf(" The previous binary of %q is kept at %q; put it back with: mv -f %s %s", s.Path, s.undo.bak, shQuote(s.undo.bak), shQuote(s.undo.target))
			s.undo.done = true
			continue
		}
		s.undo.Commit()
	}
}

func (d Deps) rollBackOne(ctx context.Context, s StaleCLI, failedPID int) (string, bool) {
	target, bak := s.undo.target, s.undo.bak
	manual := fmt.Sprintf("The previous binary is kept at %q; put it back with: mv -f %s %s", bak, shQuote(bak), shQuote(target))
	if why := replaceable(target); why != "" {
		return fmt.Sprintf("The new %q could not be rolled back (%s). %s", target, why, manual), false
	}
	if err := s.undo.Restore(); err != nil {
		return fmt.Sprintf("Rolling back %q failed (%v). %s", target, err, manual), false
	}
	in := d.Installer()
	d.say(fmt.Sprintf("Putting the previous %q back and restarting the daemon (up to %s)...", target, restartBound+d.verifyWait()))
	rctx, cancel := context.WithTimeout(ctx, restartBound)
	defer cancel()
	since := time.Now()
	if _, err := autostart.RestartRegistered(rctx, in); err != nil {
		return fmt.Sprintf("Rolled back %q to %s, but the service manager could not start the daemon again (%v): %s.", target, s.Version, err, daemonDown), false
	}
	if !d.waitForLive(ctx, since, failedPID) {
		return fmt.Sprintf("Rolled back %q to %s and restarted the daemon, but no live heartbeat followed within %s: %s.", target, s.Version, d.verifyWait(), daemonDown), false
	}
	return fmt.Sprintf("Rolled back %q to %s: the daemon is running it again. The new release did not start on this machine; running `monoagentcli update` again installs the same release and repeats this, so report it (with the daemon's log) instead.", target, s.Version), true
}
