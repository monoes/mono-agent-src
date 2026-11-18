//go:build windows

package chat

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/coderhost"
)

// startBoundSleeper starts a real child through the bound launch (the frame pipe attached, the leader pinned).
func startBoundSleeper(t *testing.T) (*boundDelivery, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), winE2ERole+"=sleep")
	b := &boundLaunch{}
	d, err := b.attach(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.start(cmd, nil); err != nil {
		d.close()
		t.Fatal(err)
	}
	t.Cleanup(func() { d.close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return d, cmd
}

// The turn's leader is ended through its own pinned handle, and every registry entry gets a handle of its own.
func TestBoundLeaderIsEndedThroughItsOwnHandleAndEntriesHoldTheirOwn(t *testing.T) {
	d, _ := startBoundSleeper(t)
	if d.proc.p == nil || !d.proc.p.Alive() {
		t.Fatal("the leader is not pinned and alive after the start")
	}
	h, err := d.proc.handle()
	if err != nil {
		t.Fatal(err)
	}
	if !h.Alive() {
		t.Fatal("the held copy does not see the leader alive")
	}
	_ = h.Close() // an entry closing its handle must not close the turn's
	if !d.proc.p.Alive() {
		t.Fatal("closing an entry's handle closed the turn's")
	}
	h2, err := d.proc.handle()
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	stopping := false
	d.setOnKill(func() { stopping = true })
	d.kill()
	deadline := time.Now().Add(15 * time.Second)
	for h2.Alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h2.Alive() {
		t.Fatal("the leader survived the kill")
	}
	if !stopping {
		t.Fatal("the turn was not marked as stopping before it was killed")
	}
}

// A placement that replaces the launcher's attributes is refused before anything exists.
func TestBoundStartRefusesAPlacementThatReplacedTheAttributes(t *testing.T) {
	for name, edit := range map[string]func(*exec.Cmd){
		"no inherited handle": func(c *exec.Cmd) { c.SysProcAttr.AdditionalInheritedHandles = nil },
		"no detach flags":     func(c *exec.Cmd) { c.SysProcAttr.CreationFlags = 0 },
		"another struct":      func(c *exec.Cmd) { c.SysProcAttr = nil },
		"no process list":     func(c *exec.Cmd) { c.SysProcAttr.ProcessAttributes = nil },
		"no thread list":      func(c *exec.Cmd) { c.SysProcAttr.ThreadAttributes = nil },
		"replaced in place":   func(c *exec.Cmd) { c.SysProcAttr.AdditionalInheritedHandles[0] = 4 },
		"an extra handle": func(c *exec.Cmd) {
			c.SysProcAttr.AdditionalInheritedHandles = append(c.SysProcAttr.AdditionalInheritedHandles, 4)
		},
	} {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), winE2ERole+"=sleep")
		d, err := (&boundLaunch{}).attach(cmd, "")
		if err != nil {
			t.Fatal(err)
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: start did not check the placement's attributes (it panicked: %v)", name, r)
				}
			}()
			err = d.start(cmd, func(c *exec.Cmd) error { edit(c); return nil })
		}()
		if err == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		d.close()
		if err == nil || cmd.Process != nil {
			t.Errorf("%s: the turn was started (%v)", name, err)
		}
	}
}

// A placement under another parent (the run's anchor, #1723) that passes a duplicate of the frame pipe made in that parent,
// announced by its number, is accepted and the turn starts there; a duplicate of another pipe, or the pipe left unchanged, is
// refused. The parent here is a ping.exe the test starts, standing in for the anchor.
func TestBoundStartAcceptsThePipesDuplicateInTheNewParent(t *testing.T) {
	parentCmd := exec.Command(`C:\Windows\System32\ping.exe`, "-n", "30", "127.0.0.1")
	if err := parentCmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parentCmd.Process.Kill(); _ = parentCmd.Wait() })
	parent, err := windows.OpenProcess(windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE, false, uint32(parentCmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(parent)
	other, otherW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	defer otherW.Close()
	for name, tc := range map[string]struct {
		src  func(own syscall.Handle) syscall.Handle
		keep bool // the pipe is not duplicated: the launcher's own number is left under the new parent
		ok   bool
	}{
		"the pipe's duplicate":     {src: func(own syscall.Handle) syscall.Handle { return own }, ok: true},
		"another pipe's duplicate": {src: func(syscall.Handle) syscall.Handle { return syscall.Handle(other.Fd()) }},
		"the pipe left unchanged":  {keep: true},
	} {
		cmd := exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), winE2ERole+"=sleep")
		d, err := (&boundLaunch{}).attach(cmd, "")
		if err != nil {
			t.Fatal(err)
		}
		err = d.start(cmd, func(c *exec.Cmd) error {
			a := c.SysProcAttr
			a.ParentProcess = syscall.Handle(parent)
			if tc.keep {
				return nil
			}
			var dup windows.Handle
			if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(tc.src(a.AdditionalInheritedHandles[0])), parent, &dup, 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
				return err
			}
			a.AdditionalInheritedHandles = []syscall.Handle{syscall.Handle(dup)}
			for i, e := range c.Env {
				if strings.HasPrefix(e, coderhost.CredFDEnv+"=") {
					c.Env[i] = coderhost.CredFDEnv + "=" + strconv.FormatUint(uint64(dup), 10)
				}
			}
			return nil
		})
		if err == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		d.close()
		if (err == nil) != tc.ok {
			t.Errorf("%s: start: %v", name, err)
		}
	}
}

// The worker the launch starts carries the locked access list: a process of the same user cannot name it as a parent, start a
// thread in it or write into it, and what the host needs (its handle, the query and wait rights) is there. The test switches off
// SeDebugPrivilege, which a service-started session can have enabled and which skips every list.
func TestBoundStartLocksTheWorkersAccessList(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatal(err)
	}
	defer tok.Close()
	for _, name := range []string{"SeDebugPrivilege", "SeTakeOwnershipPrivilege"} {
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
			t.Fatal(err)
		}
		off := windows.Tokenprivileges{PrivilegeCount: 1, Privileges: [1]windows.LUIDAndAttributes{{Luid: luid}}}
		var prev windows.Tokenprivileges
		var n uint32
		if windows.AdjustTokenPrivileges(tok, false, &off, uint32(unsafe.Sizeof(prev)), &prev, &n) == nil && prev.PrivilegeCount == 1 {
			defer windows.AdjustTokenPrivileges(tok, false, &prev, 0, nil, nil)
		}
	}
	d, cmd := startBoundSleeper(t)
	for name, rights := range map[string]uint32{
		"CREATE_PROCESS": windows.PROCESS_CREATE_PROCESS, "CREATE_THREAD": windows.PROCESS_CREATE_THREAD,
		"VM_WRITE": windows.PROCESS_VM_WRITE, "VM_OPERATION": windows.PROCESS_VM_OPERATION, "DUP_HANDLE": windows.PROCESS_DUP_HANDLE,
	} {
		h, err := windows.OpenProcess(rights, false, uint32(cmd.Process.Pid))
		if err == nil {
			windows.CloseHandle(h)
			t.Errorf("the started worker opened with %s", name)
		}
	}
	if !d.proc.p.Alive() {
		t.Fatal("the host's pinned handle cannot see the locked worker")
	}
	if sid, err := d.proc.p.UserSID(); err != nil || sid == "" {
		t.Fatalf("the locked worker's user cannot be read: %q %v", sid, err)
	}
}

// awaitExitNoReap waits for the leader's exit while the host still holds the child (cmd.Process, not yet waited for), and
// says no for anything it cannot observe; the strict turn is killed when it says no.
func TestAwaitExitNoReapWaitsForTheExitOfAHeldChild(t *testing.T) {
	_, cmd := startBoundSleeper(t)
	res := make(chan bool, 1)
	go func() { res <- awaitExitNoReap(cmd.Process.Pid) }()
	select {
	case got := <-res:
		t.Fatalf("returned %v while the child was alive", got)
	case <-time.After(300 * time.Millisecond):
	}
	_ = cmd.Process.Kill()
	select {
	case got := <-res:
		if !got {
			t.Fatal("did not report the held child's exit")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("did not return after the child exited")
	}
	if awaitExitNoReap(0x7ffffff0) {
		t.Fatal("held a process that does not exist")
	}
}

var procGetHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

func handleFlags(h uintptr, fl *uint32) error {
	r, _, e := procGetHandleInformation.Call(h, uintptr(unsafe.Pointer(fl)))
	if r == 0 {
		return e
	}
	return nil
}

// The write end of the frame pipe is not inheritable (os.Pipe makes both ends so).
func TestFramePipeWriteEndIsNotInheritable(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	d, err := (&boundLaunch{}).attach(cmd, "")
	if err != nil {
		t.Fatal(err)
	}
	defer d.close()
	var fl uint32
	if err := handleFlags(d.w.Fd(), &fl); err != nil {
		t.Fatal(err)
	}
	if fl&windows.HANDLE_FLAG_INHERIT != 0 {
		t.Fatal("the write end can be inherited")
	}
	if err := handleFlags(d.r.Fd(), &fl); err != nil || fl&windows.HANDLE_FLAG_INHERIT == 0 {
		t.Fatalf("the read end is not inheritable (%v): the child could not get it", err)
	}
}
