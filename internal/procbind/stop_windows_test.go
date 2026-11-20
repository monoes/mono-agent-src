//go:build windows

package procbind

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/procchain"
)

// winStopper is the stopper recoverV2 builds, with a short grace.
func winStopper() Stopper {
	return Stopper{StampOf: procchain.BootStamp, Same: procchain.SameStamp, Grace: 5 * time.Second}
}

// startPing starts a child that runs for a minute; the returned channel closes once its only Wait returned.
func startPing(t *testing.T) (*exec.Cmd, chan struct{}) {
	t.Helper()
	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe"), "-n", "60", "127.0.0.1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	t.Cleanup(func() { _ = c.Process.Kill(); <-done })
	return c, done
}

// running opens a handle of its own to pid and says whether that process has not exited.
func running(t *testing.T, pid int) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

func stampNow(t *testing.T, pid int) string {
	t.Helper()
	st := procchain.BootStamp(pid)
	if st == "" {
		t.Fatalf("no stamp for the child %d", pid)
	}
	return st
}

func TestWinStopLeaderEndsTheRecordedLeader(t *testing.T) {
	c, done := startPing(t)
	pid := c.Process.Pid
	res := winStopper().StopLeader(pid, stampNow(t, pid))
	if res.Outcome != StopStopped {
		t.Fatalf("outcome %v (%s), want StopStopped", res.Outcome, res.Cause)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the leader is still running after StopStopped")
	}
}

// An exited leader whose process object is still held (the parent has not waited for it): nothing to stop.
func TestWinStopLeaderOfAnExitedButHeldProcessIsAbsent(t *testing.T) {
	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe"), "-n", "60", "127.0.0.1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	stamp := stampNow(t, pid)
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := windows.WaitForSingleObject(h, 10000); err != nil {
		t.Fatal(err)
	}
	// c.Wait has not run, so the number still names the exited object
	res := winStopper().StopLeader(pid, stamp)
	if res.Outcome != StopAbsent {
		t.Fatalf("outcome %v (%s), want StopAbsent", res.Outcome, res.Cause)
	}
	_ = c.Wait()
}

// A leader that exited and whose object is gone: no process has the number.
func TestWinStopLeaderOfAGoneProcessIsAbsent(t *testing.T) {
	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), "/c", "exit", "0")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	stamp := procchain.BootStamp(pid)
	if stamp == "" {
		stamp = "start:1" // it was already over: any stamp names a process that does not exist
	}
	if err := c.Wait(); err != nil {
		t.Fatal(err)
	}
	if running(t, pid) {
		t.Skip("the number was given to another process already")
	}
	res := winStopper().StopLeader(pid, stamp)
	if res.Outcome != StopAbsent {
		t.Fatalf("outcome %v (%s), want StopAbsent", res.Outcome, res.Cause)
	}
}

// The number belongs to a process with another creation time: that is not the leader, and it is not touched.
func TestWinStopLeaderLeavesAProcessWithAnotherStamp(t *testing.T) {
	c, _ := startPing(t)
	pid := c.Process.Pid
	res := winStopper().StopLeader(pid, "start:1")
	if res.Outcome != StopAbsent {
		t.Fatalf("outcome %v (%s), want StopAbsent", res.Outcome, res.Cause)
	}
	time.Sleep(200 * time.Millisecond)
	if !running(t, pid) {
		t.Fatal("a process that is not the recorded leader was ended")
	}
}

func TestWinStopLeaderWithoutAStampIsHeld(t *testing.T) {
	c, _ := startPing(t)
	pid := c.Process.Pid
	for name, s := range map[string]struct {
		st   Stopper
		pid  int
		want string
	}{
		"no recorded stamp":  {winStopper(), pid, ""},
		"no pid":             {winStopper(), 0, "start:1"},
		"no stamp function":  {Stopper{}, pid, "start:1"},
		"unreadable current": {Stopper{StampOf: func(int) string { return "" }, Grace: time.Second}, pid, stampNow(t, pid)},
	} {
		res := s.st.StopLeader(s.pid, s.want)
		if res.Outcome != StopHeld || res.Cause == "" {
			t.Errorf("%s: outcome %v (%q), want StopHeld with a cause", name, res.Outcome, res.Cause)
		}
		if res.Cause == "the binding service needs Linux or macOS" {
			t.Errorf("%s: the cause is the old stub's", name)
		}
	}
	if !running(t, pid) {
		t.Fatal("a held stop ended the process")
	}
}

const jobLeaderEnv = "PROCBIND_WIN_JOB_LEADER"

// The helper is a turn's leader as monomind.Exec makes it: it starts a child in a job with KILL_ON_JOB_CLOSE whose only
// handle it holds (internal/monomind/proc_windows.go), names the child and waits.
func TestWinHelperJobLeader(t *testing.T) {
	if os.Getenv(jobLeaderEnv) == "" {
		t.Skip("helper process only")
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		t.Fatal(err)
	}
	c := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "ping.exe"), "-n", "60", "127.0.0.1")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(c.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.AssignProcessToJobObject(job, h); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
	os.Stdout.WriteString("CHILD " + strconv.Itoa(c.Process.Pid) + "\n")
	time.Sleep(60 * time.Second)
}

// Ending the leader ends the runtime it started in its KILL_ON_JOB_CLOSE job.
func TestWinStopLeaderEndsTheChildInTheLeadersJob(t *testing.T) {
	leader := exec.Command(os.Args[0], "-test.run=^TestWinHelperJobLeader$")
	leader.Env = append(os.Environ(), jobLeaderEnv+"=1")
	out, err := leader.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { _ = leader.Process.Kill(); <-done })
	sc := bufio.NewScanner(out)
	child := 0
	for child == 0 && sc.Scan() {
		if f, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "CHILD "); ok {
			child, _ = strconv.Atoi(f)
		}
	}
	go func() { _, _ = io.Copy(io.Discard, out); _ = leader.Wait(); close(done) }()
	if child == 0 {
		t.Fatal("the helper named no child")
	}
	// the child is pinned while the helper still holds its process object (its exec.Cmd has not waited)
	ch, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(child))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(ch)
	t.Cleanup(func() { _ = windows.TerminateProcess(ch, 1) })
	if ev, _ := windows.WaitForSingleObject(ch, 0); ev != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("the child is not running before the stop")
	}
	pid := leader.Process.Pid
	res := winStopper().StopLeader(pid, stampNow(t, pid))
	if res.Outcome != StopStopped {
		t.Fatalf("outcome %v (%s), want StopStopped", res.Outcome, res.Cause)
	}
	if ev, err := windows.WaitForSingleObject(ch, 5000); err != nil || ev != windows.WAIT_OBJECT_0 {
		t.Fatalf("the child in the leader's job is still running after the leader was stopped (wait %#x, %v)", ev, err)
	}
}
