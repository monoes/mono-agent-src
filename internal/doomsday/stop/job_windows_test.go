//go:build windows

package stop

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/inheritenv"
)

const inheritRole = "STOP_TEST_INHERIT_ROLE"

// The child of TestParentForPassesAPipeThroughTheAnchor reads the pipe named by the announcement, as a turn does.
func init() {
	if os.Getenv(inheritRole) != "read" {
		return
	}
	n, err := strconv.ParseUint(os.Getenv(inheritenv.Cred), 10, 64)
	if err != nil {
		os.Exit(3)
	}
	b, err := io.ReadAll(os.NewFile(uintptr(n), "pipe"))
	if err != nil {
		os.Exit(4)
	}
	os.Stdout.Write(b)
	os.Exit(0)
}

var procCompare = windows.NewLazySystemDLL("kernelbase.dll").NewProc("CompareObjectHandles")

// stillInAnchor says whether handle d of the anchor is still the pipe.
func stillInAnchor(t *testing.T, anchor windows.Handle, d syscall.Handle, pipe *os.File) bool {
	t.Helper()
	var back windows.Handle
	if err := windows.DuplicateHandle(anchor, windows.Handle(d), windows.CurrentProcess(), &back, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return false // closed there
	}
	defer windows.CloseHandle(back)
	if err := procCompare.Find(); err != nil {
		t.Fatal(err)
	}
	r, _, _ := procCompare.Call(uintptr(back), pipe.Fd())
	return r != 0 // the number can name another handle of the anchor's by now; only the pipe counts
}

// A real placement under an anchor passes a pipe handle on (#1723): the child starts, reads the token through the handle
// its announcement names, and once the placement is released no duplicate of the pipe is left in the anchor. A start
// that fails is released the same way.
func TestParentForPassesAPipeThroughTheAnchor(t *testing.T) {
	o := winJobOS{}
	apid, astamp, err := o.StartAnchor()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = o.KillAnchor(apid, astamp) })
	anchor, err := openChecked(apid, astamp, windows.PROCESS_DUP_HANDLE)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(anchor)
	for _, fail := range []bool{false, true} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.WriteString("token-1723")
		w.Close()
		rh := syscall.Handle(r.Fd())
		if err := syscall.SetHandleInformation(rh, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		if fail {
			cmd = exec.Command(`C:\nonexistent-1723\none.exe`)
		}
		cmd.Env = append(os.Environ(), inheritRole+"=read", inheritenv.Cred+"="+strconv.FormatUint(uint64(rh), 10))
		// no console, as every placed command: the anchor has none to share and a session without a desktop cannot make one
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, AdditionalInheritedHandles: []syscall.Handle{rh}}
		var out bytes.Buffer
		cmd.Stdout = &out
		release, err := o.ParentFor(cmd, apid, astamp)
		if err != nil {
			t.Fatal(err)
		}
		dup := cmd.SysProcAttr.AdditionalInheritedHandles[0]
		if !stillInAnchor(t, anchor, dup, r) {
			t.Fatal("the placement did not put a duplicate of the pipe in the anchor")
		}
		err = cmd.Start()
		release()
		if fail != (err != nil) {
			t.Fatalf("fail=%v: start: %v", fail, err)
		}
		if stillInAnchor(t, anchor, dup, r) {
			t.Fatalf("fail=%v: a duplicate of the pipe is still open in the anchor after the release", fail)
		}
		if !fail {
			if err := cmd.Wait(); err != nil || out.String() != "token-1723" {
				t.Fatalf("the child did not read the token through the announced handle: %v %q", err, out.String())
			}
		}
		r.Close()
	}
}

// The probe's child passes a pipe handle through the anchor, so a placement that cannot do that fails the probe.
func TestJobSelfTestPassesAHandleOnThisHost(t *testing.T) {
	if err := jobSelfTest(winJobOS{}); err != nil {
		t.Fatal(err)
	}
}
