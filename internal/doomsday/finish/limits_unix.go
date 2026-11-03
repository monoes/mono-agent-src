//go:build !windows

package finish

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func init() {
	if len(os.Args) > 4 && os.Args[1] == limitsMarker && os.Args[3] == "--" {
		limitsHelper(os.Args[2], os.Args[4], os.Args[4:])
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
