//go:build !windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// An update that is interrupted (Ctrl-C) while the restarted daemon is awaited says nothing about the daemon: both binaries
// stay and nothing is rolled back.
func TestAnInterruptedUpdateDoesNotRollTheBinaryBack(t *testing.T) {
	for name, during := range map[string]string{"the restart": "restart", "the wait": "wait"} {
		t.Run(name, func(t *testing.T) { interrupted(t, during) })
	}
}

func interrupted(t *testing.T, during string) {
	rr := newRollbackRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rr.fake.onRestart = func() { // the user interrupts; no heartbeat of the new version follows
		if during == "restart" {
			cancel()
		} else {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
	}
	list := rr.d.staleCLIs(ctx, "v0.215.7", nil, []byte(newAsset))
	du := rr.d.settleReplaced(ctx, list, rr.d.daemonAfterUpdate(ctx, "v0.215.7"), nil)
	if du == nil || du.Action != "failed" || du.Reason != "cancelled" || du.RolledBack {
		t.Fatalf("du = %+v", du)
	}
	if b, _ := os.ReadFile(rr.exe); string(b) != newAsset {
		t.Fatalf("the new binary was replaced: %q", b)
	}
	if rr.fake.restarted != 1 || !strings.Contains(du.Message, "mv -f") {
		t.Fatalf("restarted %d times; message: %s", rr.fake.restarted, du.Message)
	}
	if l := leftovers(t, rr.staleRig); len(l) != 1 {
		t.Fatalf("the previous binary was not kept: %v", l)
	}
}

// A daemon whose binary was not replaced is not restarted onto the same version and reported as failed.
func TestADaemonWhoseBinaryWasNotReplacedIsLeftAlone(t *testing.T) {
	rr := newRollbackRig(t)
	ctx := context.Background()
	du := rr.d.daemonNotReplaced(ctx, "v0.215.7", nil, nil)
	if du == nil || du.Action != "not_replaced" || !strings.Contains(du.Message, rr.exe) {
		t.Fatalf("du = %+v", du)
	}
	if du := rr.d.daemonNotReplaced(ctx, "v0.215.7", nil, []string{rr.exe}); du != nil {
		t.Fatalf("a binary updated otherwise counted as not replaced: %+v", du)
	}
	list := rr.d.staleCLIs(ctx, "v0.215.7", nil, []byte(newAsset))
	if du := rr.d.daemonNotReplaced(ctx, "v0.215.7", list, nil); du != nil {
		t.Fatalf("a replaced binary counted as not replaced: %+v", du)
	}
	rr.hb.write(os.Getpid(), "v0.215.7") // already on the release: nothing to say
	if du := rr.d.daemonNotReplaced(ctx, "v0.215.7", nil, nil); du != nil {
		t.Fatalf("du = %+v", du)
	}
}

// Each restart that can take long is announced before it starts.
func TestEachLongRestartIsAnnouncedBeforeItRuns(t *testing.T) {
	rr := newRollbackRig(t)
	var said []string
	rr.d.Say = func(m string) { said = append(said, m) }
	calls := 0
	rr.fake.onRestart = func() {
		calls++
		if calls == 2 {
			rr.hb.write(os.Getpid()+1, "v1.0.0")
		}
	}
	rr.update(t)
	if len(said) != 2 || !strings.Contains(said[0], "Restarting the daemon") || !strings.Contains(said[1], "Putting the previous") {
		t.Fatalf("said %q", said)
	}
}

// update --app with a download that holds no CLI replaces nothing, so the daemon is not restarted onto the same old binary
// and reported as failed, nor the bridge skipped for it.
func TestUpdateAppDoesNotRestartADaemonWhoseBinaryItCouldNotReplace(t *testing.T) {
	r, _ := newAppRig(t, map[string]string{"MonoAgent-linux-amd64": "new-app"}, false)
	r.hb.write(os.Getpid(), "v0.176.0")
	res, _ := runUpdateAppCmd(t, r.d, "--app", r.app, "--current", "v1.0.0")
	if res.Daemon == nil || res.Daemon.Action != "not_replaced" || r.fake.restarted != 0 {
		t.Fatalf("daemon = %+v, restarts %d", res.Daemon, r.fake.restarted)
	}
}

func TestUpdateAppAnnouncesTheDaemonRestartInItsProgress(t *testing.T) {
	r, _ := newAppRig(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}, false)
	r.fake.onRestart = func() { r.hb.write(os.Getpid(), "v9.9.9") }
	_, progress := runUpdateAppCmd(t, r.d, "--app", r.app, "--current", "v1.0.0")
	if !strings.Contains(progress, "Restarting the daemon") {
		t.Fatalf("progress:\n%s", progress)
	}
}

func releaseServer(t *testing.T) string {
	t.Helper()
	asset := updateAssetName()
	sum := sha256.Sum256([]byte(newAsset))
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":%q,"browser_download_url":"%s/bin"},{"name":"SHA256SUMS.txt","browser_download_url":"%s/sums"}]}`, asset, srv.URL, srv.URL)
		case "/bin":
			_, _ = w.Write([]byte(newAsset))
		case "/sums":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), asset)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/latest"
}

// `update` exits 7 when it leaves no daemon running, and 0 when the daemon runs (new binary, rolled back, or only slow to
// start). The error is exit code ExitDaemonDown through the CLI's own mapping.
func TestUpdateExitsSevenOnlyWhenNoDaemonIsLeftRunning(t *testing.T) {
	cases := []struct {
		name     string
		onRestar func(rr *rollbackRig, n int)
		servPID  int // when set, the service manager reports this process as running (still starting)
		want     int
	}{
		{"the daemon comes up", func(rr *rollbackRig, n int) {
			if n == 1 {
				rr.hb.write(os.Getpid(), "v9.9.9")
			}
		}, 0, 0},
		{"rolled back and running", func(rr *rollbackRig, n int) {
			if n == 2 {
				rr.hb.write(os.Getpid()+1, "v1.0.0")
			}
		}, 0, 0},
		{"rolled back and still down", func(rr *rollbackRig, n int) {}, 0, ExitDaemonDown},
		{"still starting", func(rr *rollbackRig, n int) {}, os.Getpid() + 5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := newRollbackRig(t)
			rr.newVer = "9.9.9"
			rr.d.LatestReleaseURL = releaseServer(t)
			rr.d.Version = func() string { return "v1.0.0" }
			rr.d.JSONOutput = func() bool { return false }
			rr.d.SelfPath = func() (string, error) { return rr.exe, nil }
			rr.d.VerifyWait = 200 * time.Millisecond
			if tc.servPID != 0 {
				rr.d.ServicePID = func(context.Context, autostart.Installer) (int, error) { return tc.servPID, nil }
			}
			n := 0
			rr.fake.onRestart = func() { n++; tc.onRestar(rr, n) }
			var err error
			captureStdout(t, func() { err = rr.d.run(NewCommand(rr.d)) })
			if got := clicore.ExitCodeFor(err); got != tc.want {
				t.Fatalf("exit code %d (err %v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestOnlyAFailedRestartThatLeftNoDaemonIsDown(t *testing.T) {
	for _, tc := range []struct {
		du   *DaemonUpdate
		down bool
	}{
		{nil, false},
		{&DaemonUpdate{Action: "restarted"}, false},
		{&DaemonUpdate{Action: "busy"}, false},
		{&DaemonUpdate{Action: "failed", Reason: "stopped"}, true},
		{&DaemonUpdate{Action: "failed", Reason: "no_heartbeat"}, true},
		{&DaemonUpdate{Action: "failed", Reason: "timed_out"}, true},
		{&DaemonUpdate{Action: "failed", Reason: "stopped", recovered: true}, false},
		{&DaemonUpdate{Action: "failed", Reason: "still_starting"}, false},
		{&DaemonUpdate{Action: "failed", Reason: "cancelled"}, false},
		{&DaemonUpdate{Action: "failed"}, false},
	} {
		if got := tc.du.Down(); got != tc.down {
			t.Errorf("%+v: Down() = %v, want %v", tc.du, got, tc.down)
		}
	}
}

// update --app --json keeps exit 0 with the daemon down; its outcome is in the JSON.
func TestUpdateAppKeepsExitZeroWhenTheDaemonIsDown(t *testing.T) {
	r, _ := newAppRig(t, map[string]string{"MonoAgent-linux-amd64": "new-app", "monoagentcli": "new-cli"}, false)
	r.d.VerifyWait = 200 * time.Millisecond
	res, _ := runUpdateAppCmd(t, r.d, "--app", r.app, "--current", "v1.0.0")
	if res.Daemon == nil || !res.Daemon.Down() {
		t.Fatalf("daemon = %+v", res.Daemon)
	}
}
