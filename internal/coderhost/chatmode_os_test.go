package coderhost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The open mode needs the peer-credential binding, which the binding service (procbind.Ready) offers on Linux, macOS and
// Windows (weaker there, and said so in the start confirmation). So the mode follows that readiness on every OS.
func TestOpenModeFollowsTheBindingPerOS(t *testing.T) {
	cases := []struct {
		goos    string
		ready   error
		wantOK  bool
		wantWhy string
	}{
		{"linux", nil, true, ""},
		{"darwin", nil, false, OpenModeMacOSReason},
		{"darwin", errors.New("the kernel cannot tell which process connected"), false, OpenModeMacOSReason},
		{"windows", nil, true, ""},
		{"windows", errors.New("this Windows cannot tell which process connected to a local socket"), true, ""}, // offered: the run starts unbound, with a warning
	}
	for _, c := range cases {
		t.Run(c.goos+"/"+fmt.Sprint(c.ready == nil), func(t *testing.T) {
			defer SetOpenModeReady(true)()
			defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
			defer SetOpenModeBindingProbe(c.goos, func() error { return c.ready })()
			ok, why := OpenModeAvailability()
			if ok != c.wantOK || why != c.wantWhy {
				t.Fatalf("%s: ok=%v reason=%q", c.goos, ok, why)
			}
		})
	}
}

// Where the binding service cannot run (Linux: an old kernel, another pid namespace; Windows: no peer-pid query) the mode is
// STILL offered, and OpenModeBindingWarning gives the service's own words for the "running without peer-credential binding" line.
func TestOpenModeWithoutBindingIsOfferedWithTheServiceReasonAsAWarning(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} { // macOS is refused before the service is asked (its own test)
		t.Run(goos, func(t *testing.T) {
			defer SetOpenModeReady(true)()
			defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
			defer SetOpenModeBindingProbe(goos, func() error { return errors.New("needs a newer kernel") })()
			if ok, why := OpenModeAvailability(); !ok {
				t.Fatalf("not offered: %q", why)
			}
			if w := OpenModeBindingWarning(); !strings.Contains(w, "needs a newer kernel") {
				t.Fatalf("warning = %q", w)
			}
		})
	}
}

// On macOS the run's process group cannot take the boss's turn (#1582: a child cannot join a group across sessions, and a
// new session leader cannot join one), so the mode is refused there whatever the binding service says, with the honest reason.
func TestOpenModeIsRefusedOnMacOSEvenWhenTheBindingIsReady(t *testing.T) {
	defer SetOpenModeReady(true)()
	defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
	defer SetOpenModeBindingProbe("darwin", func() error { return nil })()
	ok, why := OpenModeAvailability()
	if ok || why != OpenModeMacOSReason || !strings.Contains(why, "macOS") {
		t.Fatalf("ok=%v reason=%q", ok, why)
	}
	if OpenModeUnsupportedHere() != OpenModeMacOSReason {
		t.Fatalf("the start gate does not see it: %q", OpenModeUnsupportedHere())
	}
	defer SetOpenModeBindingProbe("linux", func() error { return nil })()
	if OpenModeUnsupportedHere() != "" {
		t.Fatalf("linux refused: %q", OpenModeUnsupportedHere())
	}
}
