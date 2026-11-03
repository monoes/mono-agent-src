//go:build linux

package stop

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const cgroupRoot = "/sys/fs/cgroup"

// ownCgroupDir returns the directory of the caller's cgroup on the unified hierarchy.
func ownCgroupDir() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(l, "0::"); ok {
			return cgroupRoot + p, nil
		}
	}
	return "", errors.New("no unified (cgroup v2) entry in /proc/self/cgroup")
}

func kernelAtLeast(major, minor int) error {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return err
	}
	var a, b int
	if _, err := fmt.Sscanf(unix.ByteSliceToString(u.Release[:]), "%d.%d", &a, &b); err != nil {
		return err
	}
	if a < major || (a == major && b < minor) {
		return fmt.Errorf("kernel %d.%d is older than %d.%d", a, b, major, minor)
	}
	return nil
}

// linuxProbeSteps are the real checks. What they do NOT prove (note 13.2 section 10 items 3 and 5): that a
// transient service with Delegate=yes can be started, and that clone3 with CLONE_INTO_CGROUP works; those need
// the real systemd user manager and are verified by hand, not here.
func linuxProbeSteps() []ProbeStep {
	return []ProbeStep{
		{"cgroup v2 unified hierarchy", func() error {
			var s unix.Statfs_t
			if err := unix.Statfs(cgroupRoot, &s); err != nil {
				return err
			}
			if s.Type != unix.CGROUP2_SUPER_MAGIC {
				return errors.New("/sys/fs/cgroup is not cgroup2")
			}
			return nil
		}},
		{"kernel 5.14 (cgroup.kill)", func() error { return kernelAtLeast(5, 14) }},
		{"cgroup v2 delegation to the user", func() error {
			dir, err := ownCgroupDir()
			if err != nil {
				return err
			}
			if !strings.Contains(dir, fmt.Sprintf("user@%d.service", os.Getuid())) {
				return errors.New("not inside the user's systemd manager subtree (no user manager)")
			}
			if err := unix.Access(dir, unix.W_OK); err != nil {
				return fmt.Errorf("the cgroup is not writable by the user (no delegation): %w", err)
			}
			return nil
		}},
		{"cgroup.kill present", func() error {
			dir, err := ownCgroupDir()
			if err != nil {
				return err
			}
			_, err = os.Stat(dir + "/cgroup.kill")
			return err
		}},
		{"pidfd_open", func() error {
			fd, err := unix.PidfdOpen(os.Getpid(), 0)
			if err != nil {
				return err
			}
			return unix.Close(fd)
		}},
	}
}
