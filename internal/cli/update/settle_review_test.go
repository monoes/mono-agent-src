//go:build !windows

package update

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
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
