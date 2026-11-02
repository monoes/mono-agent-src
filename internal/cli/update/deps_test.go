package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/release"
)

// invalidInput is the fake of the CLI's invalid-input error (exit code 3).
type invalidInput struct{ msg string }

func (e *invalidInput) Error() string { return e.msg }

func isInvalidInput(err error) bool {
	_, ok := err.(*invalidInput)
	return ok
}

// fakeHeartbeat is this home's daemon heartbeat, in memory: ok false means no daemon runs.
type fakeHeartbeat struct {
	mu sync.Mutex
	hb daemonhb.Heartbeat
	ok bool
}

func (f *fakeHeartbeat) read() (daemonhb.Heartbeat, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hb, f.ok
}

func (f *fakeHeartbeat) write(pid int, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hb, f.ok = daemonhb.Heartbeat{PID: pid, Version: version}, true
}

// fakeAutostart is the service manager: installed is whether the daemon is registered.
type fakeAutostart struct {
	installed  bool
	started    int
	restarted  int
	restartErr error
	onRestart  func()
	where      string // what Status reports as the service definition; default a path that does not exist
}

func (f *fakeAutostart) Install(context.Context) (autostart.Result, error) {
	return autostart.Result{}, nil
}
func (f *fakeAutostart) Uninstall(context.Context) error { return nil }
func (f *fakeAutostart) Status(context.Context) (bool, string) {
	if f.where != "" {
		return f.installed, f.where
	}
	return f.installed, "/x/monoagent-daemon.service"
}
func (f *fakeAutostart) Start(context.Context) error { f.started++; return nil }
func (f *fakeAutostart) Restart(context.Context) error {
	f.restarted++
	if f.onRestart != nil && f.restartErr == nil {
		f.onRestart()
	}
	return f.restartErr
}

// testDeps is a --json update with no daemon running, no pinned release key (the
// legacy path, as the CLI's TestMain sets it) and latestURL as GitHub's endpoint.
func testDeps(t *testing.T, latestURL string) Deps {
	t.Helper()
	none := &fakeHeartbeat{}
	return Deps{
		JSONOutput:       func() bool { return true },
		Version:          func() string { return "dev" },
		ReleaseClient:    func() *release.Client { return &release.Client{} },
		LatestReleaseURL: latestURL,
		InvalidInput: func(format string, a ...interface{}) error {
			return &invalidInput{msg: fmt.Sprintf(format, a...)}
		},
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		StartDetached: func(string, ...string) error { return fmt.Errorf("no detached processes in tests") },
		Heartbeat:     none.read,
		RestartBlockers: func(context.Context, int) (int, error, error) {
			return 0, nil, nil
		},
		Installer: func() autostart.Installer { return &fakeAutostart{} },
		// no monoagentcli on PATH, so an update test never runs a program of the machine it runs on
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		ServicePID: func(context.Context, autostart.Installer) (int, error) {
			return 0, fmt.Errorf("no service in tests")
		},
	}
}
