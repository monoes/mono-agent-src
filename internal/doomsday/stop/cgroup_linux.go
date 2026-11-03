//go:build linux

package stop

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// LinuxKernel is the production Kernel. It has not been exercised against a real delegated user manager in
// this change; the logic above it is tested against a fake.
type LinuxKernel struct{}

func (LinuxKernel) BootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

func (LinuxKernel) CurrentUID() uint32 { return uint32(os.Getuid()) }

func (LinuxKernel) DelegatedRoot() (string, error) {
	dir, err := ownCgroupDir()
	if err != nil {
		return "", err
	}
	marker := fmt.Sprintf("user@%d.service", os.Getuid())
	i := strings.Index(dir, marker)
	if i < 0 {
		return "", errors.New("not inside the user's systemd manager subtree")
	}
	return dir[:i+len(marker)], nil
}

func (LinuxKernel) SelfCgroup() (string, error) { return ownCgroupDir() }

func (LinuxKernel) IsAbsent(err error) bool { return errors.Is(err, unix.ENOENT) }

func (LinuxKernel) ListUnitNames(root string) ([]string, error) {
	es, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "monoagent-run-") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (LinuxKernel) ManagerControlGroup(name string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "-p", "ControlGroup", "--value", name).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func (LinuxKernel) OpenCgroup(path string) (Cgroup, error) {
	fd, err := unix.Open(path, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return &linuxCgroup{fd: fd, path: path}, nil
}

type linuxCgroup struct {
	fd   int
	path string // used only for the membership re-check of a signalled process
}

func (c *linuxCgroup) Close() error { return unix.Close(c.fd) }

func (c *linuxCgroup) Identity() (CgroupIdentity, error) {
	var st unix.Stat_t
	if err := unix.Fstat(c.fd, &st); err != nil {
		return CgroupIdentity{}, err
	}
	var fs unix.Statfs_t
	if err := unix.Fstatfs(c.fd, &fs); err != nil {
		return CgroupIdentity{}, err
	}
	return CgroupIdentity{ID: st.Ino, UID: st.Uid, IsCgroup2: fs.Type == unix.CGROUP2_SUPER_MAGIC}, nil
}

// readAt reads a file relative to the descriptor (never by path).
func (c *linuxCgroup) readAt(rel string) ([]byte, error) {
	fd, err := unix.Openat(c.fd, rel, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), rel)
	defer f.Close()
	var buf bytes.Buffer
	_, err = buf.ReadFrom(f)
	return buf.Bytes(), err
}

func join(sub, file string) string {
	if sub == "" {
		return file
	}
	return sub + "/" + file
}

func (c *linuxCgroup) Subgroups() ([]string, error) {
	// a fresh open file description of the same directory (a dup would share, and continue from, the offset of
	// the last listing)
	dfd, err := unix.Openat(c.fd, ".", unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(dfd), c.path)
	defer d.Close()
	es, err := d.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func (c *linuxCgroup) Procs(sub string) ([]int, error) {
	b, err := c.readAt(join(sub, "cgroup.procs"))
	if err != nil {
		return nil, err
	}
	var out []int
	for _, f := range strings.Fields(string(b)) {
		if n, err := strconv.Atoi(f); err == nil {
			out = append(out, n)
		}
	}
	return out, nil
}

func (c *linuxCgroup) ProcInfo(sub string, pid int) (string, string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", "", err
	}
	s := string(b)
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return "", "", errors.New("unparsable stat")
	}
	f := strings.Fields(s[j+1:])
	if len(f) < 20 {
		return "", "", errors.New("short stat")
	}
	return f[19], s[i+1 : j], nil // field 22 of the line is index 19 after the comm
}

// Signal follows the order of procbind.StopLeader: open a pidfd, check it is alive, re-read membership, poll
// again, then signal through the descriptor, never by number.
func (c *linuxCgroup) Signal(sub string, pid int, sig syscall.Signal) error {
	pfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return nil
		}
		return err
	}
	defer unix.Close(pfd)
	if !pidfdAlive(pfd) {
		return nil
	}
	procs, err := c.Procs(sub)
	if err != nil {
		return err
	}
	member := false
	for _, p := range procs {
		member = member || p == pid
	}
	if !member || !pidfdAlive(pfd) {
		return nil // not in the unit at the re-read: skipped, never signalled
	}
	return unix.PidfdSendSignal(pfd, sig, nil, 0)
}

func pidfdAlive(pfd int) bool {
	fds := []unix.PollFd{{Fd: int32(pfd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, 0)
	return err == nil && n == 0
}

func (c *linuxCgroup) Kill(sub string) error {
	fd, err := unix.Openat(c.fd, join(sub, "cgroup.kill"), unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	_, err = unix.Write(fd, []byte("1"))
	return err
}

func (c *linuxCgroup) Populated(sub string) (bool, error) {
	b, err := c.readAt(join(sub, "cgroup.events"))
	if err != nil {
		return false, err
	}
	return bytes.Contains(b, []byte("populated 1")), nil
}

func (c *linuxCgroup) Rmdir(sub string) error {
	if sub == "" {
		return errors.New("the unit's own directory is removed by the service stop, not here")
	}
	return unix.Unlinkat(c.fd, sub, unix.AT_REMOVEDIR)
}
