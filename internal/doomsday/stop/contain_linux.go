//go:build linux

package stop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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

// linuxProbeSteps are the real checks. The last one starts a real transient service with Delegate=yes and a
// process born in a sub-cgroup of it by clone3 with CLONE_INTO_CGROUP (note 13.2 section 10 items 3, 5 and 8); it
// touches only a test unit it creates and removes itself.
func linuxProbeSteps() []ProbeStep {
	return append(linuxReadOnlySteps(), selfTestStep())
}

// linuxReadOnlySteps are the probe's steps that only read the machine: they create nothing and start nothing.
func linuxReadOnlySteps() []ProbeStep {
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

// selfTestStep is the probe's last step: the one that really starts a transient unit.
func selfTestStep() ProbeStep {
	return ProbeStep{"delegated transient unit and clone3 CLONE_INTO_CGROUP self-test", func() error {
		sleepBin, err := trustedBinary("/usr/bin/sleep", "/bin/sleep")
		if err != nil {
			return err
		}
		// SIGINT and SIGTERM cancel the self-test instead of ending the process, so its unit is stopped on the way out
		// (SIGKILL cannot be caught: that unit stays until `systemctl --user stop monoagent-probe-*`).
		ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stopSignals()
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		err = selfTest(ctx, SystemHost(), LinuxKernel{}, sleepBin)
		if err == nil && ctx.Err() != nil {
			err = errors.New("the probe was interrupted")
		}
		return err
	}}
}

// ReadinessSteps are the read-only requirements of the containment unit, each with the fix to show when it fails:
// the probe's own read-only steps (so the answer is the probe's) plus the systemd-run the unit is started with.
func ReadinessSteps() []ReadinessStep {
	fixes := map[string]string{
		"cgroup v2 unified hierarchy":      "boot with systemd.unified_cgroup_hierarchy=1 (cgroup v2), or use a distribution that mounts it by default",
		"kernel 5.14 (cgroup.kill)":        "update the kernel to 5.14 or later",
		"cgroup v2 delegation to the user": "log in through a desktop session (not an ssh or tty session scope) so the user's systemd manager owns the process: `loginctl show-user $USER` must show State=active",
		"cgroup.kill present":              "update the kernel to 5.14 or later",
		"pidfd_open":                       "update the kernel to 5.3 or later",
	}
	var out []ReadinessStep
	for _, s := range linuxReadOnlySteps() {
		out = append(out, ReadinessStep{ProbeStep: s, Fix: fixes[s.Name]})
	}
	return append(out, ReadinessStep{
		ProbeStep: ProbeStep{"systemd-run available", func() error {
			_, err := trustedBinary("/usr/bin/systemd-run", "/bin/systemd-run")
			return err
		}},
		Fix: "install systemd (the package that provides /usr/bin/systemd-run, owned by root)",
	})
}
