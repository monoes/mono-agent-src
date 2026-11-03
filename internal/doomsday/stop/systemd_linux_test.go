//go:build linux

package stop

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func recordingHost(t *testing.T) (systemdHost, *[][]string) {
	t.Helper()
	var calls [][]string
	return systemdHost{run: func(_ context.Context, bin string, args ...string) error {
		calls = append(calls, append([]string{bin}, args...))
		return nil
	}}, &calls
}

func TestSystemdHostStartsADelegatedServiceInTheRootSlice(t *testing.T) {
	if _, err := trustedBinary("/usr/bin/systemd-run", "/bin/systemd-run"); err != nil {
		t.Skip("no systemd-run here: " + err.Error())
	}
	h, calls := recordingHost(t)
	name := "monoagent-run-7-" + strings.Repeat("ab", 16) + ".service"
	if err := h.StartUnit(bg, name, "desc"); err != nil {
		t.Fatal(err)
	}
	args := strings.Join((*calls)[0], " ")
	for _, want := range []string{"--user", "--unit=" + name, "--slice=-.slice", "-p Delegate=yes", "-p Type=exec", "infinity"} {
		if !strings.Contains(args, want) {
			t.Errorf("%q is missing from %q", want, args)
		}
	}
}

// A name that is not one this product creates never reaches a command line.
func TestSystemdHostRefusesAnyOtherName(t *testing.T) {
	h, calls := recordingHost(t)
	for _, name := range []string{"", "--now", "user@1000.service", "monoagent-run-7-zz.service",
		"monoagent-run-7-" + strings.Repeat("ab", 16) + ".service --now", "../monoagent-probe-" + strings.Repeat("ab", 16) + ".service"} {
		if err := h.StartUnit(bg, name, "d"); err == nil {
			t.Errorf("start accepted %q", name)
		}
		if err := h.StopUnit(bg, name); err == nil {
			t.Errorf("stop accepted %q", name)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("a command ran for a refused name: %v", *calls)
	}
}

func TestTrustedBinaryRefusesAWritableOrForeignFile(t *testing.T) {
	f := t.TempDir() + "/tool"
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := trustedBinary(f); err == nil {
		t.Fatalf("a file owned by the caller was trusted: %s", got)
	}
}

func TestSelfTestRefusesWhenTheTransientServiceCannotStart(t *testing.T) {
	k := newFakeKernel()
	h := newFakeHost(k)
	h.startErr = errors.New("Failed to connect to bus")
	err := selfTest(bg, h, k, "/usr/bin/sleep")
	if err == nil || !strings.Contains(err.Error(), "Delegate=yes") {
		t.Fatalf("err = %v", err)
	}
}

// The service started but the delegated cgroup is not there (delegation off): refused, and the service is
// stopped again.
func TestSelfTestRefusesWhenTheDelegatedCgroupIsMissing(t *testing.T) {
	k := newFakeKernel()
	k.absent = true
	h := newFakeHost(k)
	err := selfTest(bg, h, k, "/usr/bin/sleep")
	if err == nil || len(h.started) != 1 || len(h.stopped) != 1 || h.stopped[0] != h.started[0] {
		t.Fatalf("err %v started %v stopped %v", err, h.started, h.stopped)
	}
	if !strings.HasPrefix(h.started[0], "monoagent-probe-") {
		t.Fatalf("the self-test unit must not look like a run unit: %s", h.started[0])
	}
}

// The probe runs the self-test for real: a refusal here is the mode refusing to start.
func TestProbeCarriesTheSelfTestStep(t *testing.T) {
	var found bool
	for _, s := range linuxProbeSteps() {
		found = found || strings.Contains(s.Name, "CLONE_INTO_CGROUP")
	}
	if !found {
		t.Fatal("the probe does not run the delegated-unit self-test")
	}
}

// Real systemd and cgroup v2, run by hand outside the test sandbox (which hides the user bus):
//
//	MONOAGENT_DOOMSDAY_REAL_CGROUP=1 ./stop.test -test.run TestRealUnit -test.v
//
// It only ever starts and stops units it names itself: monoagent-run-<gen>-<random> with a gen no run uses, and
// monoagent-probe-<random>. Nothing else on the machine is touched.
func realEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("MONOAGENT_DOOMSDAY_REAL_CGROUP") != "1" {
		t.Skip("set MONOAGENT_DOOMSDAY_REAL_CGROUP=1 to run against the real user manager")
	}
}

func TestRealSelfTest(t *testing.T) {
	realEnv(t)
	sleepBin, err := trustedBinary("/usr/bin/sleep", "/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	if err := selfTest(bg, SystemHost(), LinuxKernel{}, sleepBin); err != nil {
		t.Fatal(err)
	}
}

func TestRealUnitCreateBornInTicketCgroupAndStop(t *testing.T) {
	realEnv(t)
	k := LinuxKernel{}
	host := SystemHost()
	dir := RunDir{Dir: t.TempDir()}
	const gen = 987654321
	c := UnitCreator{K: k, Dir: dir, Host: host}
	rec, err := c.Create(bg, gen)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = host.StopUnit(bg, rec.Name) })

	// no member yet: only the anchor, so the unit is empty
	proof, err := ProveUnit(k, rec)
	if err != nil || proof.Gone {
		t.Fatalf("the created unit does not prove: %v", err)
	}
	defer proof.Cg.Close()
	if empty, err := UnitEmpty(proof.Cg, rec); err != nil || !empty {
		t.Fatalf("a fresh unit holds more than its anchor: empty=%v err=%v", empty, err)
	}
	// a member born in a ticket cgroup makes it non-empty
	tc, err := OpenTicketCgroup(proof.Cg, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer tc.Close()
	sleepBin, _ := trustedBinary("/usr/bin/sleep", "/bin/sleep")
	cmd := exec.Command(sleepBin, "300")
	tc.Apply(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	if empty, _ := UnitEmpty(proof.Cg, rec); empty {
		t.Fatal("a unit with a live member in a ticket cgroup is called empty")
	}
	// Stop: the member is ended through the descriptor, the service stopped, the cgroup absent
	u := UnitEnder{K: k, Dir: dir, Cfg: EndConfig{Clock: SystemClock{}, TTerm: 2 * time.Second, TKill: 2 * time.Second,
		Known: func(string, int, string) bool { return true }}, StopUnit: host.StopUnit}
	out := u.End(bg, gen, nil)
	if out.Kind != UnitEnded {
		t.Fatalf("%+v", out)
	}
	if _, err := os.Stat(rec.CgroupPath); !os.IsNotExist(err) {
		t.Fatalf("the unit's cgroup is still there: %v", err)
	}
}

// Subgroups is called repeatedly on one descriptor (the stop loop, then the emptiness check): each call must see
// the directory as it is now, not continue from where the last listing ended.
func TestSubgroupsListsTheDirectoryAgainOnEveryCall(t *testing.T) {
	root := t.TempDir()
	cg, err := LinuxKernel{}.OpenCgroup(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cg.Close()
	if got, _ := cg.Subgroups(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if err := os.Mkdir(root+"/t-1", 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := cg.Subgroups(); len(got) != 1 || got[0] != "t-1" {
		t.Fatalf("a second listing on the same descriptor missed a new entry: %v", got)
	}
	if err := os.Mkdir(root+"/t-2", 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := cg.Subgroups(); len(got) != 2 {
		t.Fatalf("a third listing: %v", got)
	}
}
