// Package machine reads the machine for the resource governor: load average, available memory and free disk. The
// reading logic is platform independent and works on injected primitives, so it is tested everywhere; only the wiring
// of the real /proc and statfs primitives is Linux-only (other platforms answer governor.ErrUnsupported, which the
// caller treats as the safe minimum). Nothing here starts at import time, spawns a background goroutine, or assumes
// a cgroup.
package machine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// MaxProcRead bounds every file read; /proc/loadavg and /proc/meminfo are a few KiB at most.
const MaxProcRead = 64 << 10

// Sanity bounds: a reading outside them is an error (safe minimum), never a made-up value.
const (
	maxLoad  = 1e6
	maxCores = 1 << 16
)

// ErrBusy is returned while an earlier reading that outlived its context is still running; readings never pile up.
var ErrBusy = errors.New("doomsday governor: a previous machine reading is still running")

// Reader is the platform independent sampler over injected primitives.
type Reader struct {
	// ReadFile returns at most max bytes of the file or an error (also when the file is larger than max).
	ReadFile func(path string, max int64) ([]byte, error)
	// Statfs returns the bytes available to an unprivileged user and the total bytes of the filesystem holding dir.
	Statfs func(dir string) (free, total uint64, err error)
	// Cores returns the number of usable CPUs.
	Cores func() int
	// Dirs are the data and scratch directories; the smaller free space of them is reported. At least one.
	Dirs []string

	LoadPath string // default /proc/loadavg
	MemPath  string // default /proc/meminfo

	busy atomic.Bool
}

// Sample reads the machine. The reading runs in one goroutine that is abandoned when ctx ends (a hung statfs cannot
// hang the caller); at most one such reading exists at a time. A panic in a primitive is an error. Valid is true only
// when every field was read and is sane.
func (r *Reader) Sample(ctx context.Context) (governor.Sample, error) {
	if err := ctx.Err(); err != nil {
		return governor.Sample{}, err
	}
	if !r.busy.CompareAndSwap(false, true) {
		return governor.Sample{}, ErrBusy
	}
	type result struct {
		s   governor.Sample
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer r.busy.Store(false)
		defer func() {
			if p := recover(); p != nil {
				ch <- result{err: fmt.Errorf("doomsday governor: machine reading panicked: %v", p)}
			}
		}()
		s, err := r.read()
		ch <- result{s, err}
	}()
	select {
	case res := <-ch:
		return res.s, res.err
	case <-ctx.Done():
		return governor.Sample{}, ctx.Err()
	}
}

func (r *Reader) read() (governor.Sample, error) {
	if r.ReadFile == nil || r.Statfs == nil || r.Cores == nil {
		return governor.Sample{}, governor.ErrUnsupported
	}
	if len(r.Dirs) == 0 {
		return governor.Sample{}, errors.New("doomsday governor: no directory to measure free disk on")
	}
	lp, mp := r.LoadPath, r.MemPath
	if lp == "" {
		lp = "/proc/loadavg"
	}
	if mp == "" {
		mp = "/proc/meminfo"
	}
	lb, err := r.ReadFile(lp, MaxProcRead)
	if err != nil {
		return governor.Sample{}, err
	}
	load, err := ParseLoadAvg(lb)
	if err != nil {
		return governor.Sample{}, err
	}
	mb, err := r.ReadFile(mp, MaxProcRead)
	if err != nil {
		return governor.Sample{}, err
	}
	mem, err := ParseMemAvailFrac(mb)
	if err != nil {
		return governor.Sample{}, err
	}
	cores := r.Cores()
	if cores < 1 || cores > maxCores {
		return governor.Sample{}, fmt.Errorf("doomsday governor: implausible core count %d", cores)
	}
	s := governor.Sample{Valid: true, Load1: load, Cores: cores, MemAvailFrac: mem}
	for i, d := range r.Dirs {
		free, total, err := r.Statfs(d)
		if err != nil {
			return governor.Sample{}, err
		}
		if total == 0 || free > total {
			return governor.Sample{}, fmt.Errorf("doomsday governor: implausible statfs for %q", d)
		}
		frac := float64(free) / float64(total)
		if i == 0 || free < s.DiskFreeBytes {
			s.DiskFreeBytes = free
		}
		if i == 0 || frac < s.DiskFreeFrac {
			s.DiskFreeFrac = frac
		}
	}
	return s, nil
}

// ParseLoadAvg returns the 1-minute load average from the content of /proc/loadavg.
func ParseLoadAvg(b []byte) (float64, error) {
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, errors.New("doomsday governor: empty loadavg")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxLoad {
		return 0, fmt.Errorf("doomsday governor: bad loadavg %q", f[0])
	}
	return v, nil
}

// ParseMemAvailFrac returns MemAvailable/MemTotal (0..1) from the content of /proc/meminfo. A kernel without
// MemAvailable is an error: no estimate is invented.
func ParseMemAvailFrac(b []byte) (float64, error) {
	var total, avail uint64
	var haveT, haveA bool
	for _, line := range strings.Split(string(b), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok || (key != "MemTotal" && key != "MemAvailable") {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return 0, fmt.Errorf("doomsday governor: empty %s", key)
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("doomsday governor: bad %s %q", key, f[0])
		}
		if key == "MemTotal" {
			total, haveT = v, true
		} else {
			avail, haveA = v, true
		}
	}
	if !haveT || !haveA || total == 0 || avail > total {
		return 0, errors.New("doomsday governor: meminfo lacks a sane MemTotal/MemAvailable")
	}
	return float64(avail) / float64(total), nil
}
