//go:build windows

package stop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	nestRole = "STOP_TEST_NEST_ROLE"
	nestRec  = "STOP_TEST_NEST_REC"
	nestDir  = "STOP_TEST_NEST_DIR"
)

// The "host" of TestPlaceCommitAcrossJobChains: a process the test put in a job that forbids breakaway before it ran.
// It places a turn into a run's job of another chain (the test's), then into one it made itself, and prints both.
func init() {
	if os.Getenv(nestRole) != "host" {
		return
	}
	allowed, err := winJobOS{}.BreakawayAllowed()
	fmt.Printf("host: breakaway-allowed=%v (%v)\n", allowed, err)
	var foreign UnitRecord
	b, _ := os.ReadFile(os.Getenv(nestRec))
	if err := json.Unmarshal(b, &foreign); err != nil {
		fmt.Println("host: no record:", err)
		os.Exit(0)
	}
	fmt.Println("C foreign run job:", placeTurn(foreign))
	own, err := createJob(context.Background(), winJobOS{}, CreateArgs{Dir: RunDir{Dir: os.Getenv(nestDir)}}, 4)
	if err != nil {
		fmt.Println("D own run job: create:", err)
		os.Exit(0)
	}
	fmt.Println("D own run job:", placeTurn(own))
	_ = winJobOS{}.KillAnchor(own.AnchorPID, own.AnchorStamp)
	os.Exit(0)
}

// placeTurn starts the commitRole child the way a bound turn is started and placed (PlaceCommit, Start, commit), and
// says what became of it.
func placeTurn(rec UnitRecord) string {
	pl, err := jobContainment{os: winJobOS{}}.Place(rec)
	if err != nil {
		return "place: " + err.Error()
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err.Error()
	}
	defer r.Close()
	_, _ = w.WriteString("token-1723")
	w.Close()
	rh := syscall.Handle(r.Fd())
	_ = syscall.SetHandleInformation(rh, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, nestRole+"=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, commitRole+"=read", commitJob+"="+jobName(rec.Name), commitFD+"="+strconv.FormatUint(uint64(rh), 10))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, AdditionalInheritedHandles: []syscall.Handle{rh}}
	var out bytes.Buffer
	cmd.Stdout = &out
	commit, err := pl.PlaceCommit(cmd)
	if err != nil {
		return "placecommit: " + err.Error()
	}
	brk := cmd.SysProcAttr.CreationFlags&windows.CREATE_BREAKAWAY_FROM_JOB != 0
	serr := cmd.Start()
	cerr := commit()
	if serr != nil {
		return "start: " + serr.Error()
	}
	werr := cmd.Wait()
	if cerr != nil {
		return fmt.Sprintf("refused breakaway=%v exit=%v: %v", brk, werr, cerr)
	}
	return fmt.Sprintf("placed breakaway=%v %s exit=%v", brk, out.String(), werr)
}

// breakawayAnchors starts the anchor out of this process's jobs, so the run's job is a top-level job (the run was started
// from another job chain than the coder host).
type breakawayAnchors struct{ winJobOS }

func (breakawayAnchors) StartAnchor() (int, string, error) {
	sys, _ := windows.GetSystemDirectory()
	cmd := exec.Command(filepath.Join(sys, "ping.exe"), "-t", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_BREAKAWAY_FROM_JOB}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		return 0, "", err
	}
	defer windows.CloseHandle(h)
	s, err := procStamp(h)
	_ = cmd.Process.Release()
	return cmd.Process.Pid, s, err
}

// The nesting table of the turn placement (#1723), every case through the real PlaceCommit:
//
//	A  host in this test's job (breakaway allowed), run's job top-level       -> placed, out of the host's job
//	B  host in this test's job, run's job nested under it                     -> placed, out of the host's job
//	C  host in a job that forbids breakaway, run's job in another chain       -> refused, ended, explicit message
//	D  host in a job that forbids breakaway, run's job nested under that job  -> placed, in the host's job
//
// It needs this test to run in a job that allows breakaway (an ssh session on Windows is one).
func TestPlaceCommitAcrossJobChains(t *testing.T) {
	o := winJobOS{}
	if ok, err := o.BreakawayAllowed(); err != nil || !ok {
		t.Skipf("this process may not create children out of its job (%v %v)", ok, err)
	}
	newRun := func(jo JobOS, gen uint64) UnitRecord {
		rec, err := createJob(context.Background(), jo, CreateArgs{Dir: RunDir{Dir: t.TempDir()}}, gen)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = o.KillAnchor(rec.AnchorPID, rec.AnchorStamp) })
		return rec
	}
	const placed = "placed breakaway=true member=true token=token-1723 exit=<nil>"
	if got := placeTurn(newRun(breakawayAnchors{}, 1)); got != placed {
		t.Errorf("A top-level run job: %s", got)
	} else {
		t.Logf("A top-level run job: %s", got)
	}
	nested := newRun(o, 2)
	if got := placeTurn(nested); got != placed {
		t.Errorf("B nested run job: %s", got)
	} else {
		t.Logf("B nested run job: %s", got)
	}
	// a host in a job that forbids breakaway: this test's binary, put in such a job before it ran
	noBreak, err := o.CreateJob(testJobName(t)) // KILL_ON_JOB_CLOSE only: no BREAKAWAY_OK
	if err != nil {
		t.Fatal(err)
	}
	defer noBreak.Close()
	recFile := filepath.Join(t.TempDir(), "rec.json")
	b, _ := json.Marshal(nested)
	if err := os.WriteFile(recFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
	host := exec.Command(os.Args[0], "-test.run=^$")
	host.Env = append(os.Environ(), nestRole+"=host", nestRec+"="+recFile, nestDir+"="+t.TempDir())
	host.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	var out bytes.Buffer
	host.Stdout, host.Stderr = &out, &out
	if err := o.Suspend(host, false); err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	if err := o.AssignResume(noBreak, host); err != nil {
		_ = host.Wait()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = host.Process.Kill()
		<-done
		t.Fatal("the host did not finish")
	}
	text := out.String()
	t.Logf("host in a no-breakaway job:\n%s", text)
	for _, want := range []string{
		"host: breakaway-allowed=false",
		"C foreign run job: refused breakaway=false exit=exit status 1: ",
		"start the open-ended run from the same environment as the coder host",
		"D own run job: placed breakaway=false member=true token=token-1723 exit=<nil>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the host's output lacks %q", want)
		}
	}
}
