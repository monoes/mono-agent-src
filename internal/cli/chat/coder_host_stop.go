package chat

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// stopHost ends the host process named by info (the caller has already checked that host.json names a live process
// with the recorded start time). It reports whether the host had to be ended by force, so its own cleanup did not run.
// A variable so tests do not end a real one; the platform files set it.
var stopHost = stopHostOS

// hostStopSettle is how long `host stop` waits, after the process was ended, for the host lock to be free. It reports
// "stopped" only when the lock is.
var hostStopSettle = 5 * time.Second

// hostStopGrace is how long a host that was asked to stop gets to run its own cleanup (host.json, bindings, registry)
// before it is ended by force: its extension-shutdown wait plus a margin.
var hostStopGrace = hostStopWait + 5*time.Second

// hostStopByHand is the way out where the command cannot end the host itself: the same steps the command takes, by hand.
// No /T: that would take the running turns with the host, and they are meant to keep going.
func hostStopByHand(pid int) error {
	return fmt.Errorf("`coder host stop` cannot end the host on this system; end it by hand: its pid is %d (`monoagentcli coder host status` shows it); run `taskkill /PID %d /F` for that one process only (no /T, which would end the running turns too), then run `monoagentcli doomsday stop` again", pid, pid)
}

// hostPin is one open handle on the process host.json names. Everything the stop does to the host goes through it, so a
// pid that is reused after the check cannot be the process that is signalled or ended.
type hostPin interface {
	// Stamp is the start time of the pinned process, read from the handle.
	Stamp() string
	// Signal asks the host to stop itself (its cleanup then runs).
	Signal(instance string) error
	// WaitExit says whether the process had ended within d.
	WaitExit(d time.Duration) bool
	// Terminate ends the process at once; its cleanup does not run.
	Terminate() error
	Close()
}

// errHostGone: no process has the pid any more.
var errHostGone = errors.New("the host process is gone")

type hostStopOps struct {
	open     func(pid int) (hostPin, error)
	grace    time.Duration
	killWait time.Duration
}

// stopPinnedHost opens the host's process once, checks from that handle that it is the one host.json recorded (never
// one that has the pid now), asks it to stop, and ends it by force through the same handle only when it did not exit
// within the grace period. infoPath is named in the refusal.
func stopPinnedHost(ops hostStopOps, info hostInfo, infoPath string) (bool, error) {
	if info.PID <= 1 || info.Stamp == "" {
		return false, clicore.InvalidInput("host.json names no process I can identify; remove %s", infoPath)
	}
	pin, err := ops.open(info.PID)
	if errors.Is(err, errHostGone) {
		return false, nil // the lock is what says whether it is really over
	}
	if err != nil {
		return false, err
	}
	defer pin.Close()
	if got := pin.Stamp(); got == "" || got != info.Stamp {
		return false, clicore.InvalidInput("host.json names a process that is not the host any more; remove %s", infoPath)
	}
	if pin.WaitExit(0) {
		return false, nil
	}
	if pin.Signal(info.Instance) == nil && pin.WaitExit(ops.grace) {
		return false, nil
	}
	if err := pin.Terminate(); err != nil {
		return true, fmt.Errorf("the host did not exit in time and could not be ended: %w", err)
	}
	if !pin.WaitExit(ops.killWait) {
		return true, errors.New("the host did not exit after it was ended")
	}
	return true, nil
}

var hostInstanceRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// hostStopEventName is the name of the event a host creates for its own instance and `host stop` sets; "" for an
// instance that is not a plain id (host.json is not trusted to name anything else).
func hostStopEventName(instance string) string {
	if !hostInstanceRE.MatchString(instance) {
		return ""
	}
	return `Local\monoagent-coder-host-stop-` + instance
}
