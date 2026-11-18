//go:build windows

package stop

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/inheritenv"
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
// it is created under, so it is born in the job. The handles the command passes on are duplicated into the anchor
// (inheritable, same access) and announced by their new numbers (passInherited); release closes them there, as Go
// closes the standard handles it duplicated into the parent, then the anchor handle. It runs once whatever the
// number of calls: a second close in the anchor could hit a handle reused since, the job's own among them.
func (winJobOS) ParentFor(cmd *exec.Cmd, anchorPID int, anchorStamp string) (func(), error) {
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.ParentProcess != 0 {
		// placed twice, the announced numbers would already be the anchor's and be read as this process's handles
		return nil, errors.New("the command already has a parent process")
	}
	h, err := openChecked(anchorPID, anchorStamp, windows.PROCESS_CREATE_PROCESS|windows.PROCESS_DUP_HANDLE)
	if err != nil {
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	a := cmd.SysProcAttr
	own := make([]uintptr, len(a.AdditionalInheritedHandles))
	for i, x := range a.AdditionalInheritedHandles {
		own[i] = uintptr(x)
	}
	closeDup := func(d uintptr) {
		_ = windows.DuplicateHandle(h, windows.Handle(d), 0, nil, 0, false, windows.DUPLICATE_CLOSE_SOURCE)
	}
	dups, env, err := passInherited(own, cmd.Env, func(x uintptr) (uintptr, error) {
		var d windows.Handle
		err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(x), h, &d, 0, true, windows.DUPLICATE_SAME_ACCESS)
		return uintptr(d), err
	}, closeDup)
	if err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	if len(dups) > 0 {
		list := make([]syscall.Handle, len(dups)) // a new list: the caller compares against its own
		for i, d := range dups {
			list[i] = syscall.Handle(d)
		}
		a.AdditionalInheritedHandles, cmd.Env = list, env
	}
	a.ParentProcess = syscall.Handle(h)
	var once sync.Once
	return func() {
		once.Do(func() {
			for _, d := range dups {
				closeDup(d)
			}
			_ = windows.CloseHandle(h)
		})
	}, nil
}

// SelfTestChild starts a child under the anchor exactly as ParentFor does for a placed command, passing on the read
// end of a pipe announced the way a bound turn's credential pipe is: a placement that cannot pass a handle through the
// anchor fails the probe here instead of every turn later (#1723). The placement is released right after the start.
func (o winJobOS) SelfTestChild(anchorPID int, anchorStamp string) (int, func(), error) {
	sys, err := windows.GetSystemDirectory()
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
	if err := syscall.SetHandleInformation(syscall.Handle(w.Fd()), syscall.HANDLE_FLAG_INHERIT, 0); err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(filepath.Join(sys, "ping.exe"), "-n", "60", "127.0.0.1")
	cmd.Env = []string{inheritenv.Cred + "=" + strconv.FormatUint(uint64(rh), 10)}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, AdditionalInheritedHandles: []syscall.Handle{rh}}
	release, err := o.ParentFor(cmd, anchorPID, anchorStamp)
	if err != nil {
		return 0, nil, err
	}
	err = cmd.Start()
	release()
	if err != nil {
		return 0, nil, err
	}
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }, nil
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
