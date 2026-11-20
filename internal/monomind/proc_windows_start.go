//go:build windows

package monomind

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/monoes/mono-agent/internal/procresume"
)

// startProcessGroup starts a child this process will kill with
// killProcessGroup and puts it in a Job Object before it runs any code.
//
// The child is created with CREATE_SUSPENDED, assigned to the job, and only
// then resumed, so nothing it spawns can start outside the job. (Assigning
// after a plain Start leaves a window in which a grandchild escapes.) If the
// job cannot be made the child still runs, and killProcessGroup falls back
// to taskkill /T. If it cannot be resumed it is killed and reaped rather
// than left suspended forever, and the error is returned.
//
// Call release once the child has been waited for.
func startProcessGroup(cmd *exec.Cmd) (release func(), err error) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_SUSPENDED
	err = cmd.Start()
	cmd.SysProcAttr.CreationFlags &^= windows.CREATE_SUSPENDED
	if err != nil {
		return func() {}, err
	}
	release = attachProcessGroup(cmd)
	if rerr := resumeProcess(cmd.Process.Pid); rerr != nil {
		killProcessGroup(cmd, cmd.Process.Pid)
		_ = cmd.Wait()
		release()
		return func() {}, fmt.Errorf("resume suspended child (pid %d): %w", cmd.Process.Pid, rerr)
	}
	return release, nil
}

// resumeProcess resumes every thread of a process created with
// CREATE_SUSPENDED (it has exactly one, its main thread).
func resumeProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return procresume.Resume(h)
}

// startDetached starts a child that must outlive this process and that
// nothing here kills (OrgServeStart, OrgRunStart). It gets its own console
// process group and asks to break away from any Job Object this process is
// in, the Windows counterpart of setsid escaping a process group on unix:
// when monoagentcli itself runs inside one of our jobs (an MCP server under
// an agent CLI under a chat turn, say), killing that job must not take a
// detached org run or serve daemon with it. It is deliberately not put in a
// job of its own either: KILL_ON_JOB_CLOSE would tie its life to ours.
//
// A job that does not allow breakaway makes CreateProcess fail with
// ERROR_ACCESS_DENIED; the child is then started again without the flag,
// staying in that job as it did before. An exec.Cmd cannot be started
// twice, so the retry uses a copy and the started command is returned.
func startDetached(cmd *exec.Cmd) (*exec.Cmd, error) {
	setDetachedFlags(cmd, true)
	err := cmd.Start()
	if err == nil || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return cmd, err
	}
	retry := cloneForRetry(cmd)
	setDetachedFlags(retry, false)
	if err := retry.Start(); err != nil {
		return retry, err
	}
	return retry, nil
}

func setDetachedFlags(cmd *exec.Cmd, breakaway bool) {
	setProcessGroup(cmd)
	if breakaway {
		cmd.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	} else {
		cmd.SysProcAttr.CreationFlags &^= windows.CREATE_BREAKAWAY_FROM_JOB
	}
}
