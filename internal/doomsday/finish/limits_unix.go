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
	// The address space last: nothing allocates much between it and the exec.
	for _, r := range []struct {
		name string
		res  int
		val  uint64
	}{{"cpu", unix.RLIMIT_CPU, l.CPUSeconds}, {"nofile", unix.RLIMIT_NOFILE, l.OpenFiles}, {"fsize", unix.RLIMIT_FSIZE, l.FileSize},
		{"nproc", unix.RLIMIT_NPROC, l.Procs}, {"as", unix.RLIMIT_AS, l.AddressSpace}} {
		if err := unix.Setrlimit(r.res, &unix.Rlimit{Cur: r.val, Max: r.val}); err != nil {
			die("%s: %v", r.name, err)
		}
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
