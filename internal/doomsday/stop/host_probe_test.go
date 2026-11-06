package stop

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func withHostChecks(t *testing.T, h hostChecks) {
	t.Helper()
	old := hostChecksNow
	hostChecksNow = func() hostChecks { return h }
	t.Cleanup(func() { hostChecksNow = old })
}

func okCheck() error { return nil }

var hostStepNames = []string{"host and witness lock", "peer credentials of the emergency channel", "pinned database binding"}

// The portable guard that turns red when a host step is deleted, renamed, or left without an implementation: the
// mode may start on Windows and macOS only while all three exist and pass.
func TestHostStepsExistUnderTheirNamesAndFailWithoutAnImplementation(t *testing.T) {
	steps := hostProbeSteps(hostChecks{})
	if len(steps) != 3 {
		t.Fatalf("host steps: %d", len(steps))
	}
	for i, s := range steps {
		if s.Name != hostStepNames[i] {
			t.Errorf("step %d is %q", i, s.Name)
		}
		if s.Check() == nil {
			t.Errorf("step %q passes without an implementation", s.Name)
		}
	}
	for _, steps := range [][]ProbeStep{windowsHostSteps(), macosHostSteps()} {
		if len(steps) != 3 {
			t.Fatalf("a platform lists %d host steps", len(steps))
		}
	}
}

func TestDefaultHostChecksAreAllImplemented(t *testing.T) {
	h := hostChecksNow()
	if h.Lock == nil || h.Peer == nil || h.Pin == nil {
		t.Fatalf("a host check is missing: %+v", []bool{h.Lock != nil, h.Peer != nil, h.Pin != nil})
	}
}

func TestWindowsProbeNeedsEveryHostStep(t *testing.T) {
	for _, missing := range hostStepNames {
		w := newFakeWin()
		h := hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck}
		switch missing {
		case hostStepNames[0]:
			h.Lock = nil
		case hostStepNames[1]:
			h.Peer = nil
		default:
			h.Pin = nil
		}
		withHostChecks(t, h)
		err := jobContainment{os: w}.Probe()
		if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %q: %v", missing, err)
		}
		for _, other := range hostStepNames {
			if other != missing && strings.Contains(err.Error(), other) {
				t.Errorf("missing %q: the refusal also names %q", missing, other)
			}
		}
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck})
	if err := (jobContainment{os: newFakeWin()}).Probe(); err != nil {
		t.Fatalf("a Windows probe with every host step passing refuses: %v", err)
	}
	hostAs(t, "windows")
	swapContainment(t, KindJobObject, jobContainment{os: newFakeWin()})
	if err := probe("windows", nil); err != nil {
		t.Fatalf("Probe on a Windows host with every step passing: %v", err)
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck})
	if err := probe("windows", nil); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("Probe on a Windows host without the pin passes: %v", err)
	}
}

func TestMacOSProbeNeedsEveryHostStep(t *testing.T) {
	for _, missing := range hostStepNames {
		w := newFakePG()
		h := hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck}
		switch missing {
		case hostStepNames[0]:
			h.Lock = nil
		case hostStepNames[1]:
			h.Peer = nil
		default:
			h.Pin = nil
		}
		withHostChecks(t, h)
		err := pgContainment{os: w}.Probe()
		if !errors.Is(err, ErrContainmentUnavailable) || !strings.Contains(err.Error(), missing) {
			t.Errorf("missing %q: %v", missing, err)
		}
		if len(w.procs) != 1 {
			t.Errorf("the probe left a process behind: %d", len(w.procs))
		}
	}
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck, Pin: okCheck})
	if err := (pgContainment{os: newFakePG()}).Probe(); err != nil {
		t.Fatalf("a macOS probe with every host step passing refuses: %v", err)
	}
	hostAs(t, "darwin")
	swapContainment(t, KindPGroup, pgContainment{os: newFakePG()})
	withHostChecks(t, hostChecks{Lock: okCheck, Peer: okCheck})
	if err := probe("darwin", nil); !errors.Is(err, ErrContainmentUnavailable) {
		t.Fatalf("Probe on a macOS host without the pin passes: %v", err)
	}
}

// ----- the self-tests themselves (run for real on this OS, and against fakes that must make them fail) -----

func TestRealHostSelfTestsPassHere(t *testing.T) {
	h := hostChecksNow()
	for name, f := range map[string]func() error{"lock": h.Lock, "peer": h.Peer, "pin": h.Pin} {
		if err := f(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestLockSelfTestFailsWhenASecondHostIsNotRefused(t *testing.T) {
	if err := lockSelfTest(func(RunDir) (func(), error) { return func() {}, nil }); err == nil {
		t.Fatal("a lock that never refuses passed")
	}
	if err := lockSelfTest(func(RunDir) (func(), error) { return nil, errors.New("denied") }); err == nil {
		t.Fatal("a lock that cannot be taken passed")
	}
}

func TestPeerSelfTestFailsOnAnotherUserOrAnError(t *testing.T) {
	if err := peerSelfTest(func(net.Conn) (uint32, error) { return 0xfffffff0, nil }); err == nil {
		t.Fatal("a peer identified as someone else passed")
	}
	if err := peerSelfTest(func(net.Conn) (uint32, error) { return 0, errors.New("no creds") }); err == nil {
		t.Fatal("a peer that cannot be identified passed")
	}
}

// blindPinner pins for real but reports nothing: a binding that does not detect a swap.
type blindPinner struct{ pinner }

func (blindPinner) Check() error { return nil }
func TestPinSelfTestFailsWhenTheBindingDoesNotNoticeASwap(t *testing.T) {
	err := pinSelfTest(func(ctx context.Context, o PinOptions) (pinner, error) {
		p, err := realPinner(ctx, o)
		if err != nil {
			return nil, err
		}
		return blindPinner{p}, nil
	})
	if err == nil {
		t.Fatal("a pin that never reports a swap passed (on an OS where the system does not refuse the rename)")
	}
	if err := pinSelfTest(func(context.Context, PinOptions) (pinner, error) { return nil, errors.New("no pin") }); err == nil {
		t.Fatal("a pin that cannot be made passed")
	}
}
