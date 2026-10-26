//go:build windows

package coderhost

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// AttachMissionToken makes the token readable by cmd's child over an inherited
// pipe handle (os/exec has no ExtraFiles on Windows): the read end is marked
// inheritable and listed in SysProcAttr.AdditionalInheritedHandles, and its
// number (not secret) goes to the child in MissionFDEnv. The returned function
// closes the host's copy; call it after cmd.Start, whether Start succeeded or
// not. Callers that replace SysProcAttr afterwards must keep the list
// (clicore.DetachStart does).
func AttachMissionToken(cmd *exec.Cmd, token string) (func(), error) {
	r, err := newTokenPipe(token)
	if err != nil {
		return nil, err
	}
	h := syscall.Handle(r.Fd())
	if err := syscall.SetHandleInformation(h, syscall.HANDLE_FLAG_INHERIT, syscall.HANDLE_FLAG_INHERIT); err != nil {
		r.Close()
		return nil, err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.AdditionalInheritedHandles = append(cmd.SysProcAttr.AdditionalInheritedHandles, h)
	cmd.Env = append(cmd.Env, MissionFDEnv+"="+strconv.FormatUint(uint64(h), 10))
	return func() { r.Close() }, nil
}

func openInherited(h uintptr) (*os.File, error) {
	_ = syscall.SetHandleInformation(syscall.Handle(h), syscall.HANDLE_FLAG_INHERIT, 0) // nothing this process starts inherits it
	return os.NewFile(h, "mission-token"), nil
}

// isPipe reports whether f is a pipe: not a file, a console or a socket.
func isPipe(f *os.File) bool {
	t, err := syscall.GetFileType(syscall.Handle(f.Fd()))
	return err == nil && t == syscall.FILE_TYPE_PIPE
}
