//go:build !windows

package finish

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// RunLimitsHelperIfRequested runs the limits-helper mode when args (the command line without the program name) have
// exactly the helper's shape: marker, spec, "--", program, arguments. It never returns in that case (it execs or exits);
// otherwise it does nothing and returns.
//
// It is called from the command's run function, after the credential frame and the mission token have been read, and
// not from an init(): nothing may do I/O before main (cmd/monoagentcli init_audit_test.go).
//
// Why a model-run "monoagentcli <marker> ..." escapes nothing: the mode only LOWERS this process's resource limits
// (setrlimit without privilege cannot raise a hard limit, and a limit it cannot set ends the process with
// limitsFailExit) and then execs a program the caller could already have exec'd directly, with the caller's own
// environment and descriptors. It reads no credential, opens no database, and skips the account gate only in the way
// running any other program does: the gate judges monoagentcli commands, not programs.
func RunLimitsHelperIfRequested(args []string) {
	if len(args) > 3 && args[0] == limitsMarker && args[2] == "--" {
		limitsHelper(args[1], args[3], args[3:])
	}
}

// limitsHelper sets the limits and execs the program in place. It never returns: it execs or exits.
func limitsHelper(spec, program string, argv []string) {
	die := func(f string, a ...any) {
		fmt.Fprintf(os.Stderr, limitsFailPrefix+f+"\n", a...)
		os.Exit(limitsFailExit)
	}
	l, err := parseLimitsSpec(spec)
	if err != nil {
		die("%v", err)
	}
	if err := applyLimits(osRlimits{}, l); err != nil {
		die("%v", err)
	}
	die("exec %s: %v", program, syscall.Exec(program, argv, os.Environ()))
}

// limitsArgv wraps the program in the helper; ok is false where it cannot be (the executable is unknown).
func limitsArgv(l Limits, program string, args []string) (path string, argv []string, ok bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, false
	}
	return exe, helperArgv(exe, l, program, args), true
}

// rlimits is getrlimit/setrlimit, so the clamp is tested without changing this process's limits.
type rlimits interface {
	Get(res int) (unix.Rlimit, error)
	Set(res int, r unix.Rlimit) error
}

type osRlimits struct{}

func (osRlimits) Get(res int) (unix.Rlimit, error) {
	var r unix.Rlimit
	err := unix.Getrlimit(res, &r)
	return r, err
}

func (osRlimits) Set(res int, r unix.Rlimit) error { return unix.Setrlimit(res, &r) }

// clampLimit is the limit to set for a requested one: never above the current hard limit. An unprivileged process cannot
// raise its hard limit, and on a host whose hard limit is already lower than the request (a Mac with RLIMIT_NPROC at 2000
// against a default of 4096) the lower one is the stricter bound the sandbox wants anyway.
func clampLimit(requested, hard uint64) uint64 {
	if requested > hard {
		return hard
	}
	return requested
}

// applyLimits lowers each resource to its requested limit, clamped to the current hard limit. The address space is last:
// nothing allocates much between it and the exec. A limit that cannot be read or set is an error (the caller exits).
func applyLimits(o rlimits, l Limits) error {
	for _, r := range []struct {
		name string
		res  int
		val  uint64
	}{{"cpu", unix.RLIMIT_CPU, l.CPUSeconds}, {"nofile", unix.RLIMIT_NOFILE, l.OpenFiles}, {"fsize", unix.RLIMIT_FSIZE, l.FileSize},
		{"nproc", unix.RLIMIT_NPROC, l.Procs}, {"as", unix.RLIMIT_AS, l.AddressSpace}} {
		cur, err := o.Get(r.res)
		if err != nil {
			return fmt.Errorf("%s: %v", r.name, err)
		}
		v := clampLimit(r.val, cur.Max)
		if err := o.Set(r.res, unix.Rlimit{Cur: v, Max: v}); err != nil {
			return fmt.Errorf("%s: %v", r.name, err)
		}
	}
	return nil
}
