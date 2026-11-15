//go:build linux

package chat

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// stopStaleProcessOS pins the process with a pidfd FIRST, then checks from that pin that it is the recorded process (start time) and
// this database's coder host, and signals through the pin: a pid that is reused after the checks cannot be the one signalled. Where
// pidfd_open is not available (a kernel before 5.3) the checks run and the pid is signalled as `coder host stop` does.
func stopStaleProcessOS(cfg *Deps, info hostInfo) (string, error) {
	fd, err := unix.PidfdOpen(info.PID, 0)
	if err != nil {
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EPERM) {
			return stopStaleUnpinned(cfg, info)
		}
		return "", fmt.Errorf("the host process %d could not be pinned: %w", info.PID, err)
	}
	defer unix.Close(fd)
	if stampOf(info.PID) != info.Stamp { // read after the pin: the pinned process is the one that has this start time
		return "", errors.New("the process is not the one host.json recorded")
	}
	if err := verifyHostProcess(info.PID, cfg.DBPath()); err != nil {
		return "", err
	}
	if err := unix.PidfdSendSignal(fd, syscall.SIGTERM, nil, 0); err != nil {
		return "", fmt.Errorf("the host could not be signalled: %w", err)
	}
	return "", nil
}
