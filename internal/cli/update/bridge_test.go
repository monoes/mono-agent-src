//go:build !windows

package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A fake bridge: what answers, and a fake of the user's service manager restarting it. Nothing here touches a real bridge,
// a real service or port 9222.
type fakeBridge struct {
	state      BridgeState
	up         bool
	restarts   []string // "unit/pid" of each restart asked for
	restartErr error
	after      func(b *fakeBridge) // what a restart does: the new bridge
}

func (f *fakeBridge) deps(d Deps) Deps {
	d.VerifyWait, d.VerifyTick = 200*time.Millisecond, 10*time.Millisecond
	d.Bridge = func(context.Context) (BridgeState, bool) { return f.state, f.up }
	d.RestartBridge = func(_ context.Context, unit string, pid int) error {
		f.restarts = append(f.restarts, fmt.Sprintf("%s/%d", unit, pid))
		if f.restartErr != nil {
			return f.restartErr
		}
		if f.after != nil {
			f.after(f)
		}
		return nil
	}
	return d
}

func newFakeBridge(version string) *fakeBridge {
	return &fakeBridge{up: true, state: BridgeState{PID: 100, Version: version, Unit: "monoagent-bridge.service"},
		after: func(b *fakeBridge) { b.state.PID, b.state.Version = 101, "v0.215.7" }}
}

func TestTheBridgeIsRestartedOntoTheNewVersion(t *testing.T) {
	f := newFakeBridge("v0.176.0")
	bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
	if bu == nil || bu.Action != "restarted" || len(f.restarts) != 1 || f.restarts[0] != "monoagent-bridge.service/100" {
		t.Fatalf("bu = %+v, restarts = %v", bu, f.restarts)
	}
}

func TestTheBridgeIsLeftAloneWhenThereIsNothingToDo(t *testing.T) {
	for name, set := range map[string]func(f *fakeBridge){
		"no bridge answers":       func(f *fakeBridge) { f.up = false },
		"it already runs the new": func(f *fakeBridge) { f.state.Version = "v0.215.7" },
		"it is the daemon's own":  func(f *fakeBridge) { f.state.DaemonOwned = true },
		"a dev build":             func(f *fakeBridge) { f.state.Version = "dev" },
	} {
		f := newFakeBridge("v0.176.0")
		set(f)
		if bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7"); bu != nil || len(f.restarts) != 0 {
			t.Errorf("%s: bu = %+v, restarts = %v", name, bu, f.restarts)
		}
	}
	if bu := testDeps(t, "http://127.0.0.1:1").bridgeAfterUpdate(context.Background(), "v0.215.7"); bu != nil {
		t.Errorf("with no bridge hook: %+v", bu)
	}
}

func TestTheBridgeIsNotRestartedWhenItIsNotOursOrIsBusy(t *testing.T) {
	t.Run("not one of the user's services", func(t *testing.T) {
		f := newFakeBridge("v0.176.0")
		f.state.Unit = ""
		bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
		if bu == nil || bu.Action != "not_managed" || len(f.restarts) != 0 || !strings.Contains(bu.Message, "extension serve") {
			t.Fatalf("bu = %+v, restarts = %v", bu, f.restarts)
		}
	})
	t.Run("a command is in flight", func(t *testing.T) {
		f := newFakeBridge("v0.176.0")
		f.state.InFlight = 2
		bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
		if bu == nil || bu.Action != "busy" || len(f.restarts) != 0 || !strings.Contains(bu.Message, "systemctl --user restart monoagent-bridge.service") {
			t.Fatalf("bu = %+v, restarts = %v", bu, f.restarts)
		}
	})
}

func TestAFailedBridgeRestartSaysWhatToDo(t *testing.T) {
	t.Run("the restart is refused", func(t *testing.T) {
		f := newFakeBridge("v0.176.0")
		f.restartErr = errors.New("the bridge is no longer pid 100 run by the unit")
		bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
		if bu == nil || bu.Action != "failed" || !strings.Contains(bu.Message, "systemctl --user restart monoagent-bridge.service") {
			t.Fatalf("bu = %+v", bu)
		}
	})
	t.Run("it comes back on the old version: the unit starts another binary", func(t *testing.T) {
		f := newFakeBridge("v0.176.0")
		f.after = func(b *fakeBridge) { b.state.PID = 101 } // a new process, the same old version
		bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
		if bu == nil || bu.Action != "failed" || !strings.Contains(bu.Message, "still runs v0.176.0") || !strings.Contains(bu.Message, "systemctl --user cat") {
			t.Fatalf("bu = %+v", bu)
		}
	})
	t.Run("no bridge answers within the wait", func(t *testing.T) {
		f := newFakeBridge("v0.176.0")
		f.after = func(b *fakeBridge) { b.up = false }
		start := time.Now()
		bu := f.deps(testDeps(t, "http://127.0.0.1:1")).bridgeAfterUpdate(context.Background(), "v0.215.7")
		if bu == nil || bu.Action != "failed" || !strings.Contains(bu.Message, "no bridge answered") || time.Since(start) > 3*time.Second {
			t.Fatalf("bu = %+v after %s", bu, time.Since(start))
		}
	})
	t.Run("a restart that never returns ends at the bound", func(t *testing.T) {
		old := restartBound
		restartBound = 100 * time.Millisecond
		defer func() { restartBound = old }()
		f := newFakeBridge("v0.176.0")
		d := f.deps(testDeps(t, "http://127.0.0.1:1"))
		d.RestartBridge = func(ctx context.Context, unit string, pid int) error { <-ctx.Done(); return ctx.Err() }
		var bu *BridgeUpdate
		within(t, 5*time.Second, func() { bu = d.bridgeAfterUpdate(context.Background(), "v0.215.7") })
		if bu == nil || bu.Action != "failed" {
			t.Fatalf("bu = %+v", bu)
		}
	})
}

// When the bridge did not come up, no previous binary is dropped: each is kept, with the command that puts it back.
func TestAFailedBridgeKeepsThePreviousBinaries(t *testing.T) {
	rr := newRollbackRig(t)
	rr.fake.onRestart = func() { rr.hb.write(os.Getpid(), "v0.215.7") } // the daemon is fine
	f := newFakeBridge("v0.176.0")
	f.restartErr = errors.New("systemctl failed")
	list := rr.d.staleCLIs(context.Background(), "v0.215.7", nil, []byte(newAsset))
	d := f.deps(rr.d)
	bu := d.bridgeAfterUpdate(context.Background(), "v0.215.7")
	du := d.settleReplaced(context.Background(), list, d.daemonAfterUpdate(context.Background(), "v0.215.7"), bu)
	if du == nil || du.Action != "restarted" {
		t.Fatalf("du = %+v", du)
	}
	if kept, _ := filepath.Glob(filepath.Join(filepath.Dir(rr.exe), ".monoagentcli-previous-*")); len(kept) != 1 {
		t.Fatalf("the previous binary was dropped though the bridge did not come up: %v", kept)
	}
	if !strings.Contains(bu.Message, "kept at") || !strings.Contains(bu.Message, "mv -f") {
		t.Fatalf("the bridge message does not say where it is: %s", bu.Message)
	}
}

// update --app: the result carries what was done about the bridge, and the progress line says it.
func TestUpdateAppRestartsTheBridgeAndReportsIt(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("linux/amd64 install path")
	}
	latest := fakeAppRelease(t, tgz(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}), false)
	app, _ := appInstallDir(t)
	f := newFakeBridge("v0.176.0")
	f.after = func(b *fakeBridge) { b.state.PID, b.state.Version = 101, "v9.9.9" }
	d := f.deps(testDeps(t, latest))
	res, progress := runUpdateAppCmd(t, d, "--app", app, "--current", "v1.0.0")
	if !res.Success || res.Bridge == nil || res.Bridge.Action != "restarted" || len(f.restarts) != 1 || !strings.Contains(progress, "Restarted the extension bridge") {
		t.Fatalf("res = %+v, bridge %+v, restarts %v, progress %q", res, res.Bridge, f.restarts, progress)
	}
}

// `update` on a binary that is already current still brings a stale bridge onto it, as it does the daemon.
func TestUpdateWhenAlreadyCurrentRestartsAStaleBridge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name":"v9.9.9","assets":[]}`)
	}))
	t.Cleanup(srv.Close)
	f := newFakeBridge("v0.176.0")
	f.after = func(b *fakeBridge) { b.state.PID, b.state.Version = 101, "v9.9.9" }
	d := f.deps(testDeps(t, srv.URL))
	d.Version = func() string { return "v9.9.9" }
	d.JSONOutput = func() bool { return false }
	out := captureStdout(t, func() {
		if err := d.run(NewCommand(d)); err != nil {
			t.Fatal(err)
		}
	})
	if len(f.restarts) != 1 || !strings.Contains(out, "Restarted the extension bridge") {
		t.Fatalf("restarts %v, output:\n%s", f.restarts, out)
	}
}
