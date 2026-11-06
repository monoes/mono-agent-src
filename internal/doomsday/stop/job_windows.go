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
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(h)
	s, err := procStamp(h)
	return pid, s, err
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
	cmd.SysProcAttr.ParentProcess = syscall.Handle(h)
	return func() { _ = windows.CloseHandle(h) }, nil
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

// windowsHostSteps are what the rest of the mode still needs on Windows and does not have yet; each is a named
// refusal so the probe says exactly why the mode does not start, and each is deleted when its implementation lands.
func windowsHostSteps() []ProbeStep {
	no := func(what string) ProbeStep {
		return ProbeStep{Name: what, Check: func() error { return errors.New("not implemented on Windows yet") }}
	}
	return []ProbeStep{no("host and witness lock"), no("peer credentials of the emergency channel"), no("pinned database binding")}
}
