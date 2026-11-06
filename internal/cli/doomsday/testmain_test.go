package doomsday

import (
	"os"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The finish tests run the criteria as a child of this test binary, which has to become the limits helper when it is
// re-executed that way, as the command's run function does in production.
func TestMain(m *testing.M) {
	finish.RunLimitsHelperIfRequested(os.Args[1:])
	// These tests are written against a Linux host: a fake cgroup kernel and a fake service manager, and the bare "stopped"
	// of a unit that is proven gone. On another OS the real containment of that OS (a process group on a Mac) would be
	// chosen instead and leave real anchor processes behind; the platform's own containment is tested in the stop package.
	stop.SetHostGOOSForTest("linux")
	testhome.Main(m)
}
