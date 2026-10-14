package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const userCg = "/user.slice/user-1000.slice/user@1000.service/app.slice/"

// stubSystemctl installs a systemctl that answers `--user show --property=P
// --value -- unit` per the given script body ($3 is the property, $6 the unit).
func stubSystemctl(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd is Linux only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// fakeProc points procRoot at a temp dir and returns a writer for per-pid files.
func fakeProc(t *testing.T) func(pid int, name, content string) {
	t.Helper()
	proc := t.TempDir()
	old := procRoot
	procRoot = proc
	t.Cleanup(func() { procRoot = old })
	return func(pid int, name, content string) {
		d := filepath.Join(proc, fmt.Sprint(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBridgeUserServiceCgroupBranch(t *testing.T) {
	stubSystemctl(t, `case "$3" in
--property=MainPID) case "$6" in foot-server.service) echo 900;; *) echo 100;; esac;;
--property=ControlGroup) case "$6" in
sys.service) echo /system.slice/sys.service;;
foot-server.service) echo `+userCg+`foot-server.service;;
*) echo `+userCg+`b.service;;
esac;;
--property=ExecStart) case "$6" in
foot-server.service) echo '{ path=/usr/bin/foot ; argv[]=/usr/bin/foot --server ; }';;
*) echo '{ path=/bin/sh ; argv[]=/bin/sh -c exec monoagentcli bridge ; }';;
esac;;
esac
`)
	write := fakeProc(t)
	status := func(pid, ppid int) { write(pid, "status", fmt.Sprintf("Name:\tx\nPPid:\t%d\n", ppid)) }
	// 100 is the wrapper (MainPID of b.service); 200 is the bridge it started.
	status(100, 1)
	status(200, 100)
	write(200, "cgroup", "0::"+userCg+"b.service\n")
	write(200, "cmdline", "monoagentcli\x00bridge\x00")
	// 201: hand-started bridge that landed in foot-server's cgroup.
	status(201, 50)
	write(201, "cgroup", "0::"+userCg+"foot-server.service\n")
	write(201, "cmdline", "monoagentcli\x00bridge\x00")
	// 202: in b.service's cgroup, but no ancestor link and ExecStart does not match.
	status(202, 50)
	write(202, "cgroup", "0::"+userCg+"other.service\n")
	write(203, "cgroup", "0::/system.slice/sys.service\n")
	for _, tc := range []struct {
		name       string
		pid        int
		unit, want string
	}{
		{"MainPID is an ancestor", 200, "b.service", "b.service"},
		{"hand-started bridge in another service's cgroup", 201, "foot-server.service", ""},
		{"another unit's cgroup", 202, "b.service", ""},
		{"system service never accepted", 203, "sys.service", ""},
		{"cgroup unreadable", 204, "b.service", ""},
	} {
		if got := bridgeUserService(context.Background(), tc.pid, tc.unit); got != tc.want {
			t.Errorf("%s: bridgeUserService(%d, %q) = %q, want %q", tc.name, tc.pid, tc.unit, got, tc.want)
		}
	}
}

func TestBridgeUserServiceAcceptsByExecStart(t *testing.T) {
	stubSystemctl(t, `case "$3" in
--property=MainPID) echo 999;;
--property=ControlGroup) echo `+userCg+`b.service;;
--property=ExecStart) echo '{ path=/usr/bin/monoagentcli ; argv[]=/usr/bin/monoagentcli bridge ; }';;
esac
`)
	write := fakeProc(t)
	write(300, "status", "PPid:\t50\n")
	write(300, "cgroup", "0::"+userCg+"b.service\n")
	write(300, "cmdline", "/usr/bin/monoagentcli\x00bridge\x00")
	if got := bridgeUserService(context.Background(), 300, "b.service"); got != "b.service" {
		t.Errorf("got %q, want b.service", got)
	}
}

func TestBridgeUserServiceSystemctlErrorOnSecondCall(t *testing.T) {
	stubSystemctl(t, `case "$3" in
--property=MainPID) echo 100;;
*) exit 1;;
esac
`)
	write := fakeProc(t)
	write(200, "cgroup", "0::"+userCg+"b.service\n")
	if got := bridgeUserService(context.Background(), 200, "b.service"); got != "" {
		t.Errorf("got %q, want empty when ControlGroup lookup fails", got)
	}
}

func TestBridgeUserServiceUnloadedUnitEmptyControlGroup(t *testing.T) {
	stubSystemctl(t, `case "$3" in
--property=MainPID) echo 0;;
--property=ControlGroup) echo;;
esac
`)
	write := fakeProc(t)
	write(200, "cgroup", "0::"+userCg+"b.service\n")
	if got := bridgeUserService(context.Background(), 200, "b.service"); got != "" {
		t.Errorf("got %q, want empty for an unloaded unit", got)
	}
}
