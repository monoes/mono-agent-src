package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/daemonhb"
)

// fakePin is a process table entry: the process a pid names now. It records what was done to it.
type fakePin struct {
	stamp       string
	exitOnSig   bool // exits when signalled (the host's cleanup ran)
	exitOnTerm  bool
	termErr     error
	exited      bool
	signals     int
	terminates  int
	closed      bool
	sigInstance string
}

func (p *fakePin) Stamp() string { return p.stamp }
func (p *fakePin) Signal(i string) error {
	p.signals++
	p.sigInstance = i
	if p.exitOnSig {
		p.exited = true
	}
	return nil
}
func (p *fakePin) WaitExit(time.Duration) bool { return p.exited }
func (p *fakePin) Terminate() error {
	p.terminates++
	if p.exitOnTerm {
		p.exited = true
	}
	return p.termErr
}
func (p *fakePin) Close() { p.closed = true }

func fakeOps(p *fakePin, openErr error) (hostStopOps, *int) {
	opens := 0
	return hostStopOps{grace: time.Millisecond, killWait: time.Millisecond, open: func(int) (hostPin, error) {
		opens++
		if openErr != nil {
			return nil, openErr
		}
		return p, nil
	}}, &opens
}

var testInfo = hostInfo{PID: 4242, Stamp: "created:111", Instance: "host-ab12cd34"}

// The host that exits when asked is not ended by force: its own cleanup runs.
func TestHostStopGracefulExitDoesNotTerminate(t *testing.T) {
	p := &fakePin{stamp: "created:111", exitOnSig: true}
	ops, _ := fakeOps(p, nil)
	forced, err := stopPinnedHost(ops, testInfo, "host.json")
	if err != nil || forced {
		t.Fatalf("forced=%v err=%v, want a graceful stop", forced, err)
	}
	if p.signals != 1 || p.sigInstance != "host-ab12cd34" || p.terminates != 0 || !p.closed {
		t.Fatalf("signals %d (instance %q), terminates %d, closed %v", p.signals, p.sigInstance, p.terminates, p.closed)
	}
}

// A host that ignores the signal is ended once, through the pinned handle, and the stop says it was forced.
func TestHostStopIgnoredSignalIsTerminatedOnce(t *testing.T) {
	p := &fakePin{stamp: "created:111", exitOnTerm: true}
	ops, opens := fakeOps(p, nil)
	forced, err := stopPinnedHost(ops, testInfo, "host.json")
	if err != nil || !forced {
		t.Fatalf("forced=%v err=%v, want a forced stop", forced, err)
	}
	if p.terminates != 1 || *opens != 1 {
		t.Fatalf("terminates %d, opens %d, want 1 and 1 (the process is opened once)", p.terminates, *opens)
	}
}

// A pid that now belongs to another process (a different start time, or none readable) is neither signalled nor ended.
func TestHostStopRefusesARecycledPid(t *testing.T) {
	for _, stamp := range []string{"created:222", ""} {
		p := &fakePin{stamp: stamp, exitOnSig: true, exitOnTerm: true}
		ops, _ := fakeOps(p, nil)
		_, err := stopPinnedHost(ops, testInfo, "/x/host.json")
		if err == nil || !strings.Contains(err.Error(), "not the host any more") || !strings.Contains(err.Error(), "/x/host.json") {
			t.Fatalf("stamp %q: err %v, want the stale-host.json refusal", stamp, err)
		}
		if p.signals != 0 || p.terminates != 0 || !p.closed {
			t.Fatalf("stamp %q: signals %d, terminates %d, closed %v: the other process was touched", stamp, p.signals, p.terminates, p.closed)
		}
	}
}

// host.json without a start time, or naming pid 1 or below, is refused before any process is opened.
func TestHostStopRefusesWhatItCannotIdentifyBeforeOpening(t *testing.T) {
	for _, info := range []hostInfo{{PID: 4242, Stamp: ""}, {PID: 1, Stamp: "created:111"}, {PID: 0, Stamp: "created:111"}, {PID: -5, Stamp: "created:111"}} {
		p := &fakePin{stamp: "created:111"}
		ops, opens := fakeOps(p, nil)
		if _, err := stopPinnedHost(ops, info, "host.json"); err == nil || *opens != 0 {
			t.Fatalf("%+v: err %v, opens %d, want a refusal before any open", info, err, *opens)
		}
	}
}

// No process with the pid: nothing to end, no error (the lock says whether it is over).
func TestHostStopWithNoProcessEndsNothing(t *testing.T) {
	ops, _ := fakeOps(nil, errHostGone)
	forced, err := stopPinnedHost(ops, testInfo, "host.json")
	if err != nil || forced {
		t.Fatalf("forced=%v err=%v", forced, err)
	}
}

// A process that has exited already (its handle still opens) is not ended again.
func TestHostStopOfAnExitedProcessEndsNothing(t *testing.T) {
	p := &fakePin{stamp: "created:111", exited: true}
	ops, _ := fakeOps(p, nil)
	if _, err := stopPinnedHost(ops, testInfo, "host.json"); err != nil || p.signals != 0 || p.terminates != 0 {
		t.Fatalf("err %v signals %d terminates %d", err, p.signals, p.terminates)
	}
}

// A failed or ineffective terminate is an error, never a success.
func TestHostStopReportsAFailedTerminate(t *testing.T) {
	p := &fakePin{stamp: "created:111", termErr: os.ErrPermission}
	ops, _ := fakeOps(p, nil)
	if _, err := stopPinnedHost(ops, testInfo, "host.json"); err == nil {
		t.Fatal("a failed terminate was reported as a stop")
	}
	p = &fakePin{stamp: "created:111"} // terminate "succeeds" but the process stays
	ops, _ = fakeOps(p, nil)
	if _, err := stopPinnedHost(ops, testInfo, "host.json"); err == nil {
		t.Fatal("a process that did not exit after the terminate was reported as stopped")
	}
}

func TestHostStopEventNameOnlyForPlainInstances(t *testing.T) {
	if got := hostStopEventName("host-ab12cd34"); got != `Local\monoagent-coder-host-stop-host-ab12cd34` {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"", `..\x`, "a b", `Global\x`, "host/../x", strings.Repeat("a", 65)} {
		if got := hostStopEventName(bad); got != "" {
			t.Errorf("%q named an event: %q", bad, got)
		}
	}
}

func hostStopJSON(t *testing.T, cfg *Deps) map[string]any {
	t.Helper()
	cmd := newCoderHostCmd(cfg)
	cmd.SetArgs([]string{"stop"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("host stop: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output %q: %v", out.String(), err)
	}
	return m
}

// "stopped" is reported only once the host lock is free: an ended process whose lock is still held (something else
// holds it) is stopped=false, and a free lock is stopped=true; "forced" shows only when the host was ended by force.
func TestHostStopReportsStoppedOnlyWhenTheLockIsFree(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	if err := os.MkdirAll(hostDir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := daemonhb.LockFile(hostLockPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	raw, _ := json.Marshal(testInfo)
	if err := os.WriteFile(filepath.Join(hostDir(cfg), hostInfoFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	oldStamp, oldStop, oldSettle := stampOf, stopHost, hostStopSettle
	stampOf, hostStopSettle = func(int) string { return testInfo.Stamp }, 100*time.Millisecond
	t.Cleanup(func() { stampOf, stopHost, hostStopSettle = oldStamp, oldStop, oldSettle })

	stopHost = func(*Deps, hostInfo) (bool, error) { return true, nil } // "ended", lock still held
	if m := hostStopJSON(t, cfg); m["stopped"] != false || m["forced"] != true {
		t.Fatalf("lock held after the kill: %v, want stopped=false forced=true", m)
	}
	stopHost = func(*Deps, hostInfo) (bool, error) { release(); return false, nil } // exits, lock released
	if m := hostStopJSON(t, cfg); m["stopped"] != true || m["pid"] != float64(testInfo.PID) {
		t.Fatalf("lock free after a graceful stop: %v", m)
	} else if _, has := m["forced"]; has {
		t.Fatalf("a graceful stop says forced: %v", m)
	}
}
