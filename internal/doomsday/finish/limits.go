package finish

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The enforced resource limits of one criterion (note 13.6 section 5): CPU time, address space, open files, the size of one
// file, the number of processes, and a byte cap on the writable scratch.
//
// The process limits are applied by the child itself, before it becomes the criterion's program: a Go parent cannot set
// limits between fork and exec, and setting them on its own process would bind the host. The criterion is started as this
// executable in a one-purpose mode (limitsHelper, run from init before anything else) that sets the limits and then execs
// the program in place: same process, so the process is still BORN in the unit's ticket cgroup and nothing runs between.
// A limit the helper cannot set stops the program from starting (an error result), never a run without it.

// Limits are the limits of one criterion; a zero field takes its default.
type Limits struct {
	CPUSeconds   uint64 // CPU time (RLIMIT_CPU)
	AddressSpace uint64 // RLIMIT_AS, bytes
	OpenFiles    uint64 // RLIMIT_NOFILE
	FileSize     uint64 // RLIMIT_FSIZE: the largest single file, bytes
	Procs        uint64 // RLIMIT_NPROC: a bound per user id, so a busy user's count is in it
	ScratchBytes int64  // the cap on everything under the scratch directory
}

// Defaults of the limits (the scratch cap is the note's 256 MiB).
const (
	DefaultAddressSpace = 8 << 30
	DefaultOpenFiles    = 1024
	DefaultProcs        = 4096
	DefaultScratchBytes = 256 << 20
)

// withDefaults fills the zero fields; the CPU default is the wall-clock timeout, since a program cannot use more CPU
// time than that in one process.
func (l Limits) withDefaults(timeout time.Duration) Limits {
	if l.CPUSeconds == 0 {
		l.CPUSeconds = uint64((timeout + time.Second - 1) / time.Second)
	}
	if l.AddressSpace == 0 {
		l.AddressSpace = DefaultAddressSpace
	}
	if l.OpenFiles == 0 {
		l.OpenFiles = DefaultOpenFiles
	}
	if l.Procs == 0 {
		l.Procs = DefaultProcs
	}
	if l.ScratchBytes <= 0 {
		l.ScratchBytes = DefaultScratchBytes
	}
	if l.FileSize == 0 {
		l.FileSize = uint64(l.ScratchBytes)
	}
	return l
}

// limitsMarker is the argument that makes this executable the limits helper. limitsFailExit is the helper's own exit
// status when it could not set a limit; the harness reads it together with the stderr prefix limitsFailPrefix.
const (
	limitsMarker     = "--monoagent-finish-limits-helper"
	limitsFailExit   = 199
	limitsFailPrefix = "finish-limits: "
)

// helperArgv is the command line that runs program under the limits: the helper mode of this executable.
func helperArgv(exe string, l Limits, program string, args []string) []string {
	spec := fmt.Sprintf("%d,%d,%d,%d,%d", l.CPUSeconds, l.AddressSpace, l.OpenFiles, l.FileSize, l.Procs)
	return append([]string{exe, limitsMarker, spec, "--", program}, args...)
}

// parseLimitsSpec reads the helper's argument: five positive integers.
func parseLimitsSpec(s string) (Limits, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 5 {
		return Limits{}, fmt.Errorf("the limits spec %q has %d fields, not 5", s, len(parts))
	}
	var v [5]uint64
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || n == 0 {
			return Limits{}, fmt.Errorf("the limit %q is not a positive integer", p)
		}
		v[i] = n
	}
	return Limits{CPUSeconds: v[0], AddressSpace: v[1], OpenFiles: v[2], FileSize: v[3], Procs: v[4]}, nil
}

// scratchBytes sums the sizes of the regular files under dir, without following links, stopping at maxEntries entries so a
// directory of millions of files cannot hold the check. over is true when the sum or the entry count passed the bound.
func scratchBytes(dir string, limit int64) (total int64, over bool) {
	const maxEntries = 200000
	entries := 0
	var walk func(string)
	walk = func(d string) {
		ents, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range ents {
			if over {
				return
			}
			entries++
			if entries > maxEntries {
				over = true
				return
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			switch {
			case info.IsDir():
				walk(filepath.Join(d, e.Name()))
			case info.Mode().IsRegular():
				total += info.Size()
				over = over || total > limit
			}
		}
	}
	walk(dir)
	return total, over
}
