//go:build windows

package stop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/procresume"
)

// NOT VERIFIED on a real Windows host: this file compiles (GOOS=windows go vet) and its logic is tested through the
// JobOS fake on Linux; it has not run against the Windows API.

const (
	jobAssignProcess = 0x0001
	jobQuery         = 0x0004
	jobTerminate     = 0x0008
	jobAllAccess     = 0x1F001F
	jobPidList       = 3 // JobObjectBasicProcessIdList
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procOpenJob      = kernel32.NewProc("OpenJobObjectW")
	procIsProcInJob  = kernel32.NewProc("IsProcessInJob")
	errAnchorChanged = errors.New("the process is not the one recorded (its start time differs)")
)

func realJobOS() JobOS { return winJobOS{} }

type winJobOS struct{}

func procStamp(h windows.Handle) (string, error) {
	var c, e, k, u windows.Filetime
	if err := windows.GetProcessTimes(h, &c, &e, &k, &u); err != nil {
		return "", err
	}
	return strconv.FormatInt(c.Nanoseconds(), 10), nil
}

func (winJobOS) BootID() (string, error) {
	return strconv.FormatInt(time.Now().Add(-windows.DurationSinceBoot()).Unix(), 10), nil
}

func (winJobOS) Self() (int, string, error) {
	h, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, "", err
	}
	s, err := procStamp(h)
	return os.Getpid(), s, err
}

// openChecked opens pid with the access and checks it is the recorded process (same start time).
func openChecked(pid int, stamp string, access uint32) (windows.Handle, error) {
	h, err := windows.OpenProcess(access|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, err
	}
	if s, err := procStamp(h); err != nil || s != stamp {
		windows.CloseHandle(h)
		if err == nil {
			err = errAnchorChanged
		}
		return 0, err
	}
	return h, nil
}

func (winJobOS) Session() (int, error) {
	var sid uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &sid); err != nil {
		return 0, err
	}
	return int(sid), nil
}

func (winJobOS) StartAnchor() (int, string, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, "", err
	}
	// ping blocks without a console or input and talks only to the loopback address.
	cmd := exec.Command(filepath.Join(sys, "ping.exe"), "-t", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		return 0, "", err
	}
	// Whatever fails from here on, the process just started is ended: the caller has no pid to end it with.
	pid := cmd.Process.Pid
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Process.Release()
		return 0, "", err
	}
	defer windows.CloseHandle(h)
	s, err := procStamp(h) // read while os.Process still holds the process: the pid cannot have been reused
	if err != nil {
		_ = windows.TerminateProcess(h, 1)
		_ = cmd.Process.Release()
		return 0, "", err
	}
	_ = cmd.Process.Release()
	return pid, s, nil
}

func (winJobOS) KillAnchor(pid int, stamp string) error {
	h, err := openChecked(pid, stamp, windows.PROCESS_TERMINATE)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, errAnchorChanged) {
			return nil // gone, or the pid is another process now: nothing of ours to end
		}
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

func (winJobOS) CreateJob(name string) (Job, error) {
	if j, err := openJob(name); err == nil {
		j.Close()
		return nil, fmt.Errorf("a job object named %s already exists", name)
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateJobObject(nil, p)
	if err != nil {
		return nil, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &winJob{h: h}, nil
}

func openJob(name string) (*winJob, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	r, _, e := procOpenJob.Call(uintptr(jobQuery|jobTerminate|jobAssignProcess), 0, uintptr(unsafe.Pointer(p)))
	if r == 0 {
		if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
			return nil, ErrJobAbsent
		}
		return nil, e
	}
	return &winJob{h: windows.Handle(r)}, nil
}

func (winJobOS) OpenJob(name string) (Job, error) {
	j, err := openJob(name)
	if err != nil {
		return nil, err
	}
	return j, nil
}

func (winJobOS) Adopt(j Job, anchorPID int, anchorStamp string) error {
	wj, ok := j.(*winJob)
	if !ok {
		return errors.New("not a Windows job")
	}
	h, err := openChecked(anchorPID, anchorStamp, windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_DUP_HANDLE)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := windows.AssignProcessToJobObject(wj.h, h); err != nil {
		return err
	}
	self, err := windows.GetCurrentProcess()
	if err != nil {
		return err
	}
	var dup windows.Handle
	// the duplicate lives in the anchor: the job survives this process and dies with the anchor
	return windows.DuplicateHandle(self, wj.h, h, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS)
}

func (winJobOS) ProcInfo(pid int) (string, string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", "", err
	}
	defer windows.CloseHandle(h)
	stamp, err := procStamp(h)
	if err != nil {
		return "", "", err
	}
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return stamp, "", nil
	}
	return stamp, filepath.Base(windows.UTF16ToString(buf[:n])), nil
}

// ParentFor makes the child be created with the anchor as its parent process: a child inherits the job of the parent
// it is created under, so it is born in the job.
func (winJobOS) ParentFor(cmd *exec.Cmd, anchorPID int, anchorStamp string) (func(), error) {
	h, err := openChecked(anchorPID, anchorStamp, windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE)
	if err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if len(cmd.SysProcAttr.AdditionalInheritedHandles) != 0 {
		// they would have to exist in the anchor (Go passes them as they are, #1723), and no handle of ours is ever put
		// there: a command that passes handles is placed with PlaceCommit
		_ = windows.CloseHandle(h)
		return nil, errors.New("a command that passes inherited handles is not created under the anchor")
	}
	cmd.SysProcAttr.ParentProcess = syscall.Handle(h)
	return func() { _ = windows.CloseHandle(h) }, nil
}

func (winJobOS) Suspend(cmd *exec.Cmd, breakaway bool) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	if cmd.SysProcAttr.ParentProcess != 0 {
		return errors.New("the command already has another parent process")
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	if breakaway {
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return nil
}

// BreakawayAllowed asks Windows itself: a job's own limit says nothing of the jobs it is nested in, and any of them can
// refuse. The throwaway child (cmd.exe, no handles, no console) is created suspended and ended before it ran.
func (winJobOS) BreakawayAllowed() (bool, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return false, err
	}
	c := exec.Command(filepath.Join(sys, "cmd.exe"), "/c", "exit", "0")
	c.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NO_WINDOW}
	if err := c.Start(); procresume.BreakawayRefused(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("whether a turn may leave this process's job cannot be told: %w", err)
	}
	_ = c.Process.Kill()
	_, _ = c.Process.Wait()
	return true, nil
}

// AssignResume works through the handle CreateProcess returned to this process (os.Process.WithHandle): it has every
// right whatever access list the process was created with, and it names this process and no other (a number could be
// reused). A process in a job can only join a job nested under it; one created out of every job can join any.
func (winJobOS) AssignResume(j Job, cmd *exec.Cmd) error {
	wj, ok := j.(*winJob)
	if !ok {
		return errors.New("not a Windows job")
	}
	var err error
	if herr := cmd.Process.WithHandle(func(ph uintptr) {
		h := windows.Handle(ph)
		var in int32
		if err = windows.AssignProcessToJobObject(wj.h, h); err != nil {
			err = fmt.Errorf("the job refused the process: %w", err)
		} else if r, _, e := procIsProcInJob.Call(ph, uintptr(wj.h), uintptr(unsafe.Pointer(&in))); r == 0 || in == 0 {
			err = fmt.Errorf("the process is not a member of the job after the assignment (%v)", e)
		} else {
			err = procresume.Resume(h)
		}
		if err != nil {
			_ = windows.TerminateProcess(h, 1)
		}
	}); herr != nil {
		return herr // waited for or released already: there is no handle to place it with
	}
	return err
}

// SelfTestTurn starts ping.exe the way the bound launcher starts a turn (coder_host_bound_windows.go: the worker's locked
// access lists, detached, the read end of a pipe as its one inherited handle) and places it as PlaceCommit does, out of
// this process's job when that is allowed.
func (o winJobOS) SelfTestTurn(j Job) (int, func(), error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, nil, err
	}
	pa, ta, err := procbind.WorkerAttributes()
	if err != nil {
		return 0, nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()
	defer w.Close()
	rh := syscall.Handle(r.Fd())
	if err := syscall.SetHandleInformation(rh, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(filepath.Join(sys, "ping.exe"), "-n", "60", "127.0.0.1")
	const flags = windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags, AdditionalInheritedHandles: []syscall.Handle{rh}, ProcessAttributes: pa, ThreadAttributes: ta}
	breakaway, err := o.BreakawayAllowed()
	if err != nil {
		return 0, nil, err
	}
	if err := o.Suspend(cmd, breakaway); err != nil {
		return 0, nil, err
	}
	if err := cmd.Start(); err != nil {
		return 0, nil, err
	}
	if err := o.AssignResume(j, cmd); err != nil {
		_, _ = cmd.Process.Wait()
		return 0, nil, fmt.Errorf("%w (created out of this process's job: %v)", err, breakaway)
	}
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }, nil
}

// SelfTestChild starts a child under the anchor exactly as ParentFor does for a placed command.
func (o winJobOS) SelfTestChild(anchorPID int, anchorStamp string) (int, func(), error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(filepath.Join(sys, "ping.exe"), "-n", "60", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	release, err := o.ParentFor(cmd, anchorPID, anchorStamp)
	if err != nil {
		return 0, nil, err
	}
	if err := cmd.Start(); err != nil {
		release()
		return 0, nil, err
	}
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait(); release() }, nil
}

type winJob struct{ h windows.Handle }

func (j *winJob) Close() error { return windows.CloseHandle(j.h) }

func (j *winJob) Terminate() error { return windows.TerminateJobObject(j.h, 1) }

func (j *winJob) Pids() ([]int, error) {
	for n := 64; n <= 1<<16; n *= 16 {
		hdr := 8 / int(unsafe.Sizeof(uintptr(0))) // JOBOBJECT_BASIC_PROCESS_ID_LIST: two DWORD counts, then the ids
		buf := make([]uintptr, hdr+n)
		var ret uint32
		err := windows.QueryInformationJobObject(j.h, jobPidList, uintptr(unsafe.Pointer(&buf[0])), uint32(len(buf))*uint32(unsafe.Sizeof(uintptr(0))), &ret)
		if errors.Is(err, windows.ERROR_MORE_DATA) {
			continue
		}
		if err != nil {
			return nil, err
		}
		counts := (*[2]uint32)(unsafe.Pointer(&buf[0]))
		var pids []int
		for _, v := range buf[hdr : hdr+int(counts[1])] {
			pids = append(pids, int(v))
		}
		return pids, nil
	}
	return nil, errors.New("the job holds too many processes to list")
}

func (j *winJob) Contains(pid int) (bool, error) {
	pids, err := j.Pids()
	for _, p := range pids {
		if p == pid {
			return true, nil
		}
	}
	return false, err
}

func (j *winJob) TerminateMember(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var in int32
	if r, _, e := procIsProcInJob.Call(uintptr(h), uintptr(j.h), uintptr(unsafe.Pointer(&in))); r == 0 {
		return e
	}
	if in == 0 {
		return errors.New("the process is not a member of the job")
	}
	return windows.TerminateProcess(h, 1)
}
