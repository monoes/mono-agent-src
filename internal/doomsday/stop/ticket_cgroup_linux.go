//go:build linux

package stop

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// TicketCgroup is the sub-cgroup t-<ticket> of a proven run unit (note 13.2 section 3). A process started with
// Apply is born in it by clone3 with CLONE_INTO_CGROUP: it is never in another cgroup, not even for an instant,
// so there is no fork-then-move race.
type TicketCgroup struct {
	sub string
	fd  int
}

// OpenTicketCgroup creates t-<ticket> under the unit (the descriptor comes from ProveUnit) and opens it.
func OpenTicketCgroup(unit Cgroup, ticket uint64) (*TicketCgroup, error) {
	lc, ok := unit.(*linuxCgroup)
	if !ok {
		return nil, errors.New("stop: the unit is not a Linux cgroup descriptor")
	}
	sub := fmt.Sprintf("t-%d", ticket)
	if err := unix.Mkdirat(lc.fd, sub, 0o755); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(lc.fd, sub, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		_ = unix.Unlinkat(lc.fd, sub, unix.AT_REMOVEDIR)
		return nil, err
	}
	return &TicketCgroup{sub: sub, fd: fd}, nil
}

// Name is the sub-cgroup's name inside the unit (the key of Cgroup.Procs, Kill and Rmdir).
func (t *TicketCgroup) Name() string { return t.sub }

// Apply makes cmd start inside the sub-cgroup. The ticket cgroup must stay open until cmd.Start returned.
func (t *TicketCgroup) Apply(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = t.fd
}

// Close releases the descriptor; the cgroup itself stays until removed.
func (t *TicketCgroup) Close() error { return unix.Close(t.fd) }
