//go:build !windows

package coderhost

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// missionChildFD is the descriptor the child finds the token pipe on: the first
// ExtraFiles entry.
const missionChildFD = 3

// AttachMissionToken makes the token readable by cmd's child over a pipe on
// descriptor 3: it writes the token and a newline to a fresh pipe, closes the
// write end and passes the read end to the child. The returned function closes
// the host's copy of the read end; call it after cmd.Start, whether Start
// succeeded or not. cmd.ExtraFiles must be empty (the descriptor number is fixed).
func AttachMissionToken(cmd *exec.Cmd, token string) (func(), error) {
	if len(cmd.ExtraFiles) != 0 {
		return nil, errors.New("extra files already set")
	}
	r, err := newTokenPipe(token)
	if err != nil {
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{r}
	cmd.Env = append(cmd.Env, MissionFDEnv+"="+strconv.Itoa(missionChildFD))
	return func() { r.Close() }, nil
}

func openInherited(fd uintptr) (*os.File, error) {
	syscall.CloseOnExec(int(fd))           // nothing this process starts inherits it, even before it is closed
	_ = syscall.SetNonblock(int(fd), true) // so closing the file interrupts a read that is stuck
	return os.NewFile(fd, "mission-token"), nil
}

// isPipe reports whether f is a pipe: not a regular file, a terminal or a socket.
func isPipe(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeNamedPipe != 0
}
