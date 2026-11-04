//go:build linux

package machine

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// New returns the Linux sampler reading /proc/loadavg, /proc/meminfo and statfs of dirs (the data and scratch
// directories). It is only constructed by the integrator; there is no package-level instance.
func New(dirs ...string) governor.Sampler {
	return &Reader{ReadFile: readBounded, Statfs: statfs, Cores: runtime.NumCPU, Dirs: dirs}
}

func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("doomsday governor: %s is larger than %d bytes", path, max)
	}
	return b, nil
}

func statfs(dir string) (free, total uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}
