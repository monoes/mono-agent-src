//go:build windows

package stop

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	commitRole = "STOP_TEST_COMMIT_ROLE"
	commitJob  = "STOP_TEST_COMMIT_JOB"
	commitFD   = "STOP_TEST_COMMIT_FD"
)

// The child of TestAssignResumePutsTheTurnInTheJobBeforeItRuns: the first thing it does is ask whether it is in the run's
// job, then it reads the token through the handle its environment names, as a turn reads its credential frame.
func init() {
	if os.Getenv(commitRole) != "read" {
		return
	}
	j, err := openJob(os.Getenv(commitJob))
	member := false
	if err == nil {
		var in int32
		if r, _, _ := procIsProcInJob.Call(uintptr(windows.CurrentProcess()), uintptr(j.h), uintptr(unsafe.Pointer(&in))); r != 0 {
			member = in != 0
		}
	}
	n, err := strconv.ParseUint(os.Getenv(commitFD), 10, 64)
	if err != nil {
		os.Exit(3)
	}
	b, err := io.ReadAll(os.NewFile(uintptr(n), "pipe"))
	if err != nil {
		os.Exit(4)
	}
	fmt.Printf("member=%v token=%s", member, b)
	os.Exit(0)
}

func testJobName(t *testing.T) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "monoagent-test-" + hex.EncodeToString(b[:])
}

// A turn placed the PlaceCommit way (#1723): created suspended as this process's own child with an inherited pipe handle,
// put in a real named job, then resumed. Its first instruction of its own already runs inside the job, and it reads the
// token through the handle it inherited from this process (nothing was put in an anchor).
func TestAssignResumePutsTheTurnInTheJobBeforeItRuns(t *testing.T) {
	o := winJobOS{}
	name := testJobName(t)
	j, err := o.CreateJob(name)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, _ = w.WriteString("token-1723")
	w.Close()
	rh := syscall.Handle(r.Fd())
	if err := syscall.SetHandleInformation(rh, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), commitRole+"=read", commitJob+"="+name, commitFD+"="+strconv.FormatUint(uint64(rh), 10))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, AdditionalInheritedHandles: []syscall.Handle{rh}}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := o.Suspend(cmd, false); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := o.AssignResume(j, cmd); err != nil {
		_ = cmd.Wait()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil || out.String() != "member=true token=token-1723" {
		t.Fatalf("the turn did not run inside the job with its token: %v %q", err, out.String())
	}
}

// A job the process may not join (its job chain is not the one the run's job is nested in) refuses the assignment:
// AssignResume reports it and the process is ended without ever running. It needs this test to run inside a job (an ssh
// session on Windows is one), so the run's job can be made a top-level job the child cannot be nested into.
func TestAssignResumeEndsAProcessTheJobRefuses(t *testing.T) {
	var in int32
	if r, _, e := procIsProcInJob.Call(uintptr(windows.CurrentProcess()), 0, uintptr(unsafe.Pointer(&in))); r == 0 || in == 0 {
		t.Skipf("this process is in no job, so no assignment can be refused (%v)", e)
	}
	o := winJobOS{}
	j, err := o.CreateJob(testJobName(t))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	sys, _ := windows.GetSystemDirectory()
	// a process outside this process's job puts the run's job outside it too, the way an anchor started elsewhere would
	outside := exec.Command(filepath.Join(sys, "ping.exe"), "-n", "30", "127.0.0.1")
	outside.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := outside.Start(); err != nil {
		t.Skipf("this process's job allows no breakaway: %v", err)
	}
	defer func() { _ = outside.Process.Kill(); _ = outside.Wait() }()
	var aerr error
	_ = outside.Process.WithHandle(func(h uintptr) { aerr = windows.AssignProcessToJobObject(j.(*winJob).h, windows.Handle(h)) })
	if aerr != nil {
		t.Fatal(aerr)
	}
	cmd := exec.Command(filepath.Join(sys, "cmd.exe"), "/c", "exit", "0")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := o.Suspend(cmd, false); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err = o.AssignResume(j, cmd)
	werr := cmd.Wait()
	if err == nil {
		t.Fatal("the job accepted a process from another job chain")
	}
	if ee, ok := werr.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
		t.Fatalf("the refused process was not ended by the placement (it ran?): %v", werr)
	}
	t.Logf("refused as expected: %v", err)
}

// A command that passes inherited handles is never created under the anchor: they would have to sit there.
func TestParentForRefusesACommandThatPassesHandles(t *testing.T) {
	o := winJobOS{}
	pid, stamp, err := o.Self() // a live process with its real stamp stands in for the anchor
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("x")
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{5}}
	if _, err := o.ParentFor(cmd, pid, stamp); err == nil || !strings.Contains(err.Error(), "inherited handles") || cmd.SysProcAttr.ParentProcess != 0 {
		t.Fatalf("a command passing a handle was prepared under the anchor: %v", err)
	}
}

// The probe's two children (a command under the anchor, a turn put in the job) are both members of the job here.
func TestJobSelfTestOnThisHost(t *testing.T) {
	if err := jobSelfTest(winJobOS{}); err != nil {
		t.Fatal(err)
	}
}
