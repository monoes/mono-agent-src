package chat

import (
	"fmt"
	"time"
)

// stopHost ends the host process named by info (the caller has already checked that host.json names a live process
// with the recorded start time). It reports whether the host had to be ended by force, so its own cleanup did not run.
// A variable so tests do not end a real one; the platform files set it.
var stopHost = stopHostOS

// hostStopSettle is how long `host stop` waits, after the process was ended, for the host lock to be free. It reports
// "stopped" only when the lock is.
var hostStopSettle = 5 * time.Second

// hostStopByHand is the way out where the command cannot end the host itself: the same steps the command takes, by hand.
// No /T: that would take the running turns with the host, and they are meant to keep going.
func hostStopByHand(pid int) error {
	return fmt.Errorf("`coder host stop` cannot end the host on this system; end it by hand: its pid is %d (`monoagentcli coder host status` shows it); run `taskkill /PID %d /F` for that one process only (no /T, which would end the running turns too), then run `monoagentcli doomsday stop` again", pid, pid)
}
