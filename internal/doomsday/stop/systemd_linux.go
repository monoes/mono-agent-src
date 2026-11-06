//go:build linux

package stop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// SystemHost is the production UnitHost: a transient service of the user's systemd manager.
func SystemHost() UnitHost { return systemdHost{run: runCommand} }

// systemdHost starts the run unit with `systemd-run --user`. The service is placed in the manager's root slice so
// its cgroup is a direct child of the delegated root (the path the identity proof implies, P2), is delegated
// (Delegate=yes, so the host may create sub-cgroups) and runs one anchor process that blocks until the service
// is stopped. The anchor is the system's own `sleep`, taken from a root-owned location that no other user can
// write; it runs no user code and no tool of the run.
type systemdHost struct {
	run func(ctx context.Context, bin string, args ...string) error
}

var startableName = regexp.MustCompile(`^monoagent-(run-[0-9]+|probe)-[0-9a-f]{32}\.service$`)

func runCommand(ctx context.Context, bin string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// trustedBinary returns the first candidate that is a regular file owned by root and writable by no one else.
func trustedBinary(candidates ...string) (string, error) {
	for _, c := range candidates {
		p, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o022 != 0 {
			continue
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != 0 {
			continue
		}
		return p, nil
	}
	return "", fmt.Errorf("none of %v is a root-owned file that others cannot write", candidates)
}

func (h systemdHost) StartUnit(ctx context.Context, name, description string) error {
	if !startableName.MatchString(name) {
		return fmt.Errorf("%q is not a unit name this product creates", name)
	}
	runner, err := trustedBinary("/usr/bin/systemd-run", "/bin/systemd-run")
	if err != nil {
		return fmt.Errorf("systemd-run: %w", err)
	}
	anchor, err := trustedBinary("/usr/bin/sleep", "/bin/sleep")
	if err != nil {
		return fmt.Errorf("the anchor program: %w", err)
	}
	return h.run(ctx, runner, "--user", "--quiet", "--collect", "--unit="+name, "--slice=-.slice",
		"--description="+description, "-p", "Delegate=yes", "-p", "Type=exec", "-p", "KillMode=control-group",
		anchor, "infinity")
}

func (h systemdHost) StopUnit(ctx context.Context, name string) error {
	if !startableName.MatchString(name) {
		return fmt.Errorf("%q is not a unit name this product creates", name)
	}
	ctl, err := trustedBinary("/usr/bin/systemctl", "/bin/systemctl")
	if err != nil {
		return fmt.Errorf("systemctl: %w", err)
	}
	return h.run(ctx, ctl, "--user", "stop", name)
}

// selfTest is item 3, 5 and 8 of note 13.2 section 10, run for real: a transient delegated service starts, its
// cgroup appears under the delegated root, a process is born in a sub-cgroup of it by clone3 with
// CLONE_INTO_CGROUP, the sub-cgroup is killed by cgroup.kill and removed, and the service is stopped. The first
// failing item is the refusal. Only the test unit it creates is touched.
func selfTest(ctx context.Context, h UnitHost, k Kernel, sleepBin string) (err error) {
	nb := make([]byte, 16)
	if _, err := rand.Read(nb); err != nil {
		return err
	}
	name := "monoagent-probe-" + hex.EncodeToString(nb) + ".service"
	if err := h.StartUnit(ctx, name, "monoagent open-ended mode self-test"); err != nil {
		return fmt.Errorf("a transient service with Delegate=yes cannot be started: %w", err)
	}
	defer func() {
		// Not the probe's own context: an interrupted or timed-out probe has a cancelled one, and the unit must go anyway.
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if serr := h.StopUnit(stopCtx, name); serr != nil {
			err = errors.Join(err, fmt.Errorf("the self-test service %s could not be stopped: %w", name, serr))
		}
	}()
	root, err := k.DelegatedRoot()
	if err != nil {
		return err
	}
	cg, err := k.OpenCgroup(filepath.Join(root, name))
	if err != nil {
		return fmt.Errorf("the delegated cgroup of the test service is not there: %w", err)
	}
	defer cg.Close()
	tc, err := OpenTicketCgroup(cg, 0)
	if err != nil {
		return fmt.Errorf("a sub-cgroup cannot be created in the delegated cgroup: %w", err)
	}
	defer tc.Close()
	cmd := exec.Command(sleepBin, "60")
	tc.Apply(cmd)
	if err := cmd.Start(); err != nil {
		_ = cg.Rmdir(tc.Name())
		return fmt.Errorf("a process cannot be born in a sub-cgroup (clone3 CLONE_INTO_CGROUP): %w", err)
	}
	pids, perr := cg.Procs(tc.Name())
	born := false
	for _, p := range pids {
		born = born || p == cmd.Process.Pid
	}
	kerr := cg.Kill(tc.Name())
	if kerr != nil {
		_ = cmd.Process.Kill() // the test child only; it is ours
	}
	_ = cmd.Wait()
	switch {
	case perr != nil || !born:
		_ = cg.Rmdir(tc.Name())
		return errors.New("the process was not born in the sub-cgroup it was started into")
	case kerr != nil:
		return fmt.Errorf("cgroup.kill does not work in the delegated cgroup: %w", kerr)
	}
	if !waitEmpty(cg, tc.Name(), SystemClock{}, 5*time.Second, 20*time.Millisecond) {
		return errors.New("the killed sub-cgroup did not empty")
	}
	if err := cg.Rmdir(tc.Name()); err != nil {
		return fmt.Errorf("the emptied sub-cgroup cannot be removed: %w", err)
	}
	return nil
}
