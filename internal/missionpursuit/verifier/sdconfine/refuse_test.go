package sdconfine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Every way this computer can fail to confine ends in a typed refusal that names the missing piece (so the host's status line
// can say what is missing, and so verifier keeps "no-confinement").
func TestEachMissingPieceIsATypedRefusalThatNamesIt(t *testing.T) {
	for name, tc := range map[string]struct {
		mut     func(*fakeSys)
		missing Missing
		detail  string
	}{
		"not linux":      {func(f *fakeSys) { f.goos = "darwin" }, MissingPlatform, "darwin"},
		"windows":        {func(f *fakeSys) { f.goos = "windows" }, MissingPlatform, "windows"},
		"no systemd-run": {func(f *fakeSys) { f.trust = map[string]bool{"systemd-run": true} }, MissingSystemdRun, "systemd-run"},
		"no systemctl":   {func(f *fakeSys) { f.trust = map[string]bool{"systemctl": true} }, MissingSystemctl, "systemctl"},
		"no runtime dir": {func(f *fakeSys) { f.runtime = "" }, MissingUserManager, "XDG_RUNTIME_DIR"},
		"relative dir":   {func(f *fakeSys) { f.runtime = "run/user/1000" }, MissingUserManager, "XDG_RUNTIME_DIR"},
		"foreign dir":    {func(f *fakeSys) { f.rtErr = errors.New("owned by another user") }, MissingUserManager, "owned by another user"},
		"no bus":         {func(f *fakeSys) { f.busErr = errors.New("no such file") }, MissingUserManager, "bus"},
		"manager silent": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemctl" {
					return runResult{Code: 1, Stderr: "Failed to connect to bus: No medium found"}
				}
				return runResult{Code: -1}
			}
		}, MissingUserManager, "Failed to connect"},
		"version unreadable": {func(f *fakeSys) { f.version = "garbage" }, MissingUserManager, "version"},
		"unknown property": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Code: 1, Stderr: "Unknown assignment: ProtectProc=invisible\n"}
				}
				return runResult{Code: -1}
			}
		}, MissingProperty, "ProtectProc"},
		"invalid setting": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Code: 1, Stderr: "Failed to start transient service unit: Invalid PrivatePIDs setting: yes"}
				}
				return runResult{Code: -1}
			}
		}, MissingProperty, "PrivatePIDs"},
		"no memory controller": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Stdout: "memory=max\npids=512\ncpu=200000 100000\n"}
				}
				return runResult{Code: -1}
			}
		}, MissingController, "memory"},
		"no pids controller": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Stdout: "memory=2147483648\npids=max\ncpu=200000 100000\n"}
				}
				return runResult{Code: -1}
			}
		}, MissingController, "pids"},
		"no cpu controller": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Stdout: "memory=2147483648\npids=512\ncpu=\n"}
				}
				return runResult{Code: -1}
			}
		}, MissingController, "cpu"},
		"probe unit failed for another reason": {func(f *fakeSys) {
			f.handler = func(bin string, a []string) runResult {
				if bin == "systemd-run" {
					return runResult{Code: 1, Stderr: "Failed to start transient service unit: Access denied"}
				}
				return runResult{Code: -1}
			}
		}, MissingUserManager, "Access denied"},
	} {
		f := newFakeSys()
		tc.mut(f)
		c, err := newWith(context.Background(), Config{ScratchBase: t.TempDir()}, f.sys())
		var u *Unavailable
		if c != nil || !errors.As(err, &u) || u.Missing != tc.missing || !strings.Contains(err.Error(), tc.detail) {
			t.Errorf("%s: confiner %v err %v (want %s mentioning %q)", name, c, err, tc.missing, tc.detail)
			continue
		}
		if !IsUnavailable(err) || !strings.Contains(err.Error(), string(tc.missing)) {
			t.Errorf("%s: the refusal does not name the missing piece: %v", name, err)
		}
	}
}

func TestAWorkingSystemYieldsAConfinerAndTheProbeUsesTheFullPropertyList(t *testing.T) {
	f := newFakeSys()
	base := t.TempDir()
	c, err := newWith(context.Background(), Config{ScratchBase: base}, f.sys())
	if err != nil || c == nil {
		t.Fatal(err)
	}
	if c.Name() != "systemd-run" {
		t.Fatalf("name %q", c.Name())
	}
	var probe string
	for _, call := range f.called() {
		if strings.HasPrefix(call, "systemd-run ") {
			probe = call
		}
	}
	if probe == "" {
		t.Fatalf("no probe unit was run: %v", f.called())
	}
	// the probe is the real property list (a property the manager does not accept is found here, not at the first check)
	for _, p := range Properties(Spec{Work: base, Lim: probeLimits(), Caps: c.caps, PrivatePIDs: true}) {
		if !strings.Contains(probe, "-p "+p+" ") && !strings.HasSuffix(probe, "-p "+p) {
			t.Errorf("the probe does not carry %s", p)
		}
	}
	// and it names no secret and no home
	if strings.Contains(probe, "s3cret") {
		t.Errorf("the probe carries the host environment: %s", probe)
	}
}

func TestAScratchBaseThatIsNotAnExistingSafeFolderIsAPlainErrorNotARefusal(t *testing.T) {
	for _, base := range []string{"", "relative", "/nonexistent-dir-for-sdconfine", "/tmp/with space", "/tmp/a:b"} {
		_, err := newWith(context.Background(), Config{ScratchBase: base}, newFakeSys().sys())
		if err == nil || IsUnavailable(err) {
			t.Errorf("%q: %v", base, err)
		}
	}
}

func TestOldSystemdSkipsPrivatePIDsAndSaysSo(t *testing.T) {
	f := newFakeSys()
	f.version = "255.4-1"
	c := newConfiner(t, f, Config{})
	if c.privatePIDs {
		t.Fatal("PrivatePIDs asked of a manager older than 256")
	}
	if _, err := newWith(context.Background(), Config{ScratchBase: t.TempDir()}, newFakeSys().sys()); err != nil {
		t.Fatal(err)
	}
	f2 := newFakeSys()
	c2 := newConfiner(t, f2, Config{})
	if !c2.privatePIDs {
		t.Fatal("PrivatePIDs not asked of systemd 262")
	}
}
