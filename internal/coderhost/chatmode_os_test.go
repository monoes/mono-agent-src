package coderhost

import (
	"errors"
	"testing"
)

// The open mode needs the peer-credential binding, which the binding service (procbind.Ready) only offers on Linux. So the
// mode is not offered where the binding cannot be turned on, with one fixed reason, and the cheap check is not asked.
func TestOpenModeFollowsTheBindingPerOS(t *testing.T) {
	cases := []struct {
		goos    string
		ready   error
		wantOK  bool
		wantWhy string
	}{
		{"linux", nil, true, ""},
		{"darwin", errors.New("the binding service needs Linux"), false, OpenModeNoBindingReason},
		{"windows", errors.New("the binding service needs Linux"), false, OpenModeNoBindingReason},
	}
	for _, c := range cases {
		t.Run(c.goos, func(t *testing.T) {
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

// On Linux a machine that cannot run the binding service (an old kernel, another pid namespace) is refused with the
// service's own words, not the OS sentence.
func TestOpenModeLinuxWithoutBindingKeepsTheServiceReason(t *testing.T) {
	defer SetOpenModeReady(true)()
	defer SetOpenModeCheck(func() (bool, string) { return true, "" })()
	defer SetOpenModeBindingProbe("linux", func() error { return errors.New("needs Linux 6.5 or later") })()
	ok, why := OpenModeAvailability()
	if ok || why == "" || why == OpenModeNoBindingReason {
		t.Fatalf("ok=%v reason=%q", ok, why)
	}
}

// The text the person reads.
func TestOpenModeNoBindingReasonText(t *testing.T) {
	const want = "not available on this OS yet: the run needs the peer-credential binding, which exists on Linux only"
	if OpenModeNoBindingReason != want {
		t.Fatalf("%q", OpenModeNoBindingReason)
	}
}
