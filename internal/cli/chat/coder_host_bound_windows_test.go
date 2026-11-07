//go:build windows

package chat

import (
	"os"
	"os/exec"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
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
	if d.proc == nil || !d.proc.Alive() {
		t.Fatal("the leader is not pinned and alive after the start")
	}
	h, err := d.holdWorker()
	if err != nil {
		t.Fatal(err)
	}
	if !h.Alive() {
		t.Fatal("the held copy does not see the leader alive")
	}
	_ = h.Close() // an entry closing its handle must not close the turn's
	if !d.proc.Alive() {
		t.Fatal("closing an entry's handle closed the turn's")
	}
	h2, err := d.holdWorker()
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
