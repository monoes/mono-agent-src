package coderhost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The open mode needs the peer-credential binding, which the binding service (procbind.Ready) offers on Linux and macOS.
// So the mode follows that readiness there, is refused on Windows with one fixed OS reason, and the cheap check is not
// asked where it is refused.
func TestOpenModeFollowsTheBindingPerOS(t *testing.T) {
	cases := []struct {
		goos    string
		ready   error
		wantOK  bool
		wantWhy string
	}{
		{"linux", nil, true, ""},
		{"darwin", nil, true, ""},
		{"darwin", errors.New("the kernel cannot tell which process connected"), false, "not available on this machine: the run needs the peer-credential binding, and the kernel cannot tell which process connected"},
		{"windows", nil, false, OpenModeNoBindingReason},
		{"windows", errors.New("the binding service needs Linux or macOS"), false, OpenModeNoBindingReason},
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

// Where the binding service cannot run (Linux: an old kernel, another pid namespace; macOS: no LOCAL_PEERPID) the machine
// is refused with the service's own words, not the OS sentence.
func TestOpenModeWithoutBindingKeepsTheServiceReason(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			defer SetOpenModeReady(true)()
			defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
			defer SetOpenModeBindingProbe(goos, func() error { return errors.New("needs a newer kernel") })()
			ok, why := OpenModeAvailability()
			if ok || why == "" || why == OpenModeNoBindingReason || !strings.Contains(why, "needs a newer kernel") {
				t.Fatalf("ok=%v reason=%q", ok, why)
			}
		})
	}
}

// The text the person reads.
func TestOpenModeNoBindingReasonText(t *testing.T) {
	const want = "not available on Windows yet: the run needs the peer-credential binding, which Windows does not have"
	if OpenModeNoBindingReason != want {
		t.Fatalf("%q", OpenModeNoBindingReason)
	}
}
