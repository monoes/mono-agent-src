//go:build linux

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/extension"
)

// The bridge lookups and the systemctl call are faked: nothing here reads a real bridge, runs systemctl or touches port 9222.
type bridgeFakes struct {
	st          extension.Status
	up          bool
	cgroup      string
	userService string
	restarted   []string
	restartErr  error
}

func fakeBridge(t *testing.T, f *bridgeFakes) {
	t.Helper()
	oldF, oldU, oldC, oldS := findRunningBridge, userServiceOf, cgroupServiceOf, systemctlRestart
	findRunningBridge = func(context.Context) (extension.Status, string, bool) { return f.st, "http://127.0.0.1:9222", f.up }
	userServiceOf = func(context.Context, int, string) string { return f.userService }
	cgroupServiceOf = func(int) string { return f.cgroup }
	systemctlRestart = func(_ context.Context, unit string) ([]byte, error) {
		f.restarted = append(f.restarted, unit)
		return []byte("boom"), f.restartErr
	}
	t.Cleanup(func() { findRunningBridge, userServiceOf, cgroupServiceOf, systemctlRestart = oldF, oldU, oldC, oldS })
}

func TestFindBridgeSaysWhyThereIsNoUnit(t *testing.T) {
	for name, c := range map[string]struct {
		f       bridgeFakes
		unit    string
		why     string
		service string
	}{
		"a verified user service":          {bridgeFakes{up: true, st: extension.Status{PID: 5}, cgroup: "monoagent-bridge.service", userService: "monoagent-bridge.service"}, "monoagent-bridge.service", "", ""},
		"started by hand":                  {bridgeFakes{up: true, st: extension.Status{PID: 5}}, "", "not_a_service", ""},
		"a service the user manager can't": {bridgeFakes{up: true, st: extension.Status{PID: 5}, cgroup: "monoagent-bridge.service"}, "", "unverified", "monoagent-bridge.service"},
	} {
		fake := c.f
		fakeBridge(t, &fake)
		b, ok := FindBridge(context.Background(), 0)
		if !ok || b.Unit != c.unit || b.Why != c.why || b.ServiceName != c.service {
			t.Errorf("%s: %+v, %v", name, b, ok)
		}
	}
	f := bridgeFakes{up: true, st: extension.Status{PID: 9}}
	fakeBridge(t, &f)
	if b, _ := FindBridge(context.Background(), 9); !b.DaemonOwned {
		t.Error("the daemon's own bridge is not marked")
	}
	f.up = false
	if _, ok := FindBridge(context.Background(), 0); ok {
		t.Error("a bridge that does not answer was found")
	}
}

func TestRestartBridgeUnitChecksBeforeItRestartsAndRestartsOnlyThatUnit(t *testing.T) {
	ok := func() *bridgeFakes {
		return &bridgeFakes{up: true, st: extension.Status{PID: 5}, cgroup: "monoagent-bridge.service", userService: "monoagent-bridge.service"}
	}
	t.Run("restarts exactly the unit it was given, after telling", func(t *testing.T) {
		f := ok()
		fakeBridge(t, f)
		var told []string
		if err := RestartBridgeUnit(context.Background(), "monoagent-bridge.service", 5, func(s string) { told = append(told, s) }); err != nil {
			t.Fatal(err)
		}
		if len(f.restarted) != 1 || f.restarted[0] != "monoagent-bridge.service" || len(told) != 1 {
			t.Fatalf("restarted %v, told %v", f.restarted, told)
		}
	})
	for name, mod := range map[string]func(f *bridgeFakes){
		"the bridge is another process now": func(f *bridgeFakes) { f.st.PID = 6 },
		"the bridge no longer answers":      func(f *bridgeFakes) { f.up = false },
		"the user manager does not confirm": func(f *bridgeFakes) { f.userService = "" },
		"the unit that owns it is another":  func(f *bridgeFakes) { f.userService = "other.service" },
		"a command is in flight":            func(f *bridgeFakes) { f.st.InFlight = 1 },
	} {
		t.Run("refused: "+name, func(t *testing.T) {
			f := ok()
			mod(f)
			fakeBridge(t, f)
			told := false
			if err := RestartBridgeUnit(context.Background(), "monoagent-bridge.service", 5, func(string) { told = true }); err == nil {
				t.Fatal("restarted")
			}
			if len(f.restarted) != 0 || told {
				t.Fatalf("restarted %v, told %v", f.restarted, told)
			}
		})
	}
	t.Run("an empty unit is refused", func(t *testing.T) {
		f := ok()
		fakeBridge(t, f)
		if err := RestartBridgeUnit(context.Background(), "", 5, nil); err == nil || len(f.restarted) != 0 {
			t.Fatalf("err = %v, restarted %v", err, f.restarted)
		}
	})
	t.Run("a failing systemctl is reported with its output", func(t *testing.T) {
		f := ok()
		f.restartErr = errors.New("exit 1")
		fakeBridge(t, f)
		if err := RestartBridgeUnit(context.Background(), "monoagent-bridge.service", 5, nil); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTheRestartCommandAddressesTheUserManagerAndEndsOptionsBeforeTheUnit(t *testing.T) {
	got := strings.Join(bridgeRestartArgs("monoagent-bridge.service"), " ")
	if got != "--user restart -- monoagent-bridge.service" {
		t.Fatalf("args = %q", got)
	}
	if got := bridgeRestartArgs("--now")[3]; got != "--now" || bridgeRestartArgs("--now")[2] != "--" {
		t.Fatal("a unit name that looks like an option is not kept after --")
	}
}
