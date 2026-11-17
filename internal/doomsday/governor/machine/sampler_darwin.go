//go:build darwin

package machine

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// probeTimeout bounds each helper command; the sampler's own context bounds the whole reading.
const probeTimeout = 3 * time.Second

// New returns the macOS sampler: load from `sysctl vm.loadavg`, memory from `vm_stat` over `sysctl hw.memsize`
// and statfs of dirs. Before this existed macOS answered ErrUnsupported and every run sat at the safe minimum.
func New(dirs ...string) governor.Sampler {
	return &Reader{LoadFn: darwinLoad, MemFn: darwinMem, Statfs: darwinStatfs, Cores: runtime.NumCPU, Dirs: dirs}
}

func probe(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("doomsday governor: %s failed: %w", name, err)
	}
	if len(out) > MaxProcRead {
		return nil, fmt.Errorf("doomsday governor: %s output larger than %d bytes", name, MaxProcRead)
	}
	return out, nil
}

func darwinLoad() (float64, error) {
	b, err := probe("/usr/sbin/sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, err
	}
	return ParseDarwinLoadAvg(b)
}

func darwinMem() (float64, error) {
	tb, err := probe("/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, err
	}
	total, err := strconv.ParseUint(strings.TrimSpace(string(tb)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("doomsday governor: bad hw.memsize %q", strings.TrimSpace(string(tb)))
	}
	vb, err := probe("/usr/bin/vm_stat")
	if err != nil {
		return 0, err
	}
	return ParseDarwinMemAvailFrac(vb, total)
}

func darwinStatfs(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}
