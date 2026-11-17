package machine

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseDarwinLoadAvg returns the 1-minute load average from `sysctl -n vm.loadavg`, e.g. "{ 2.50 3.10 4.00 }".
func ParseDarwinLoadAvg(b []byte) (float64, error) {
	f := strings.Fields(strings.NewReplacer("{", " ", "}", " ").Replace(string(b)))
	if len(f) < 1 {
		return 0, errors.New("doomsday governor: empty vm.loadavg")
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > maxLoad {
		return 0, fmt.Errorf("doomsday governor: bad vm.loadavg %q", f[0])
	}
	return v, nil
}

// ParseDarwinMemAvailFrac returns the available fraction of memory from `vm_stat` output and the total bytes
// (hw.memsize): (free + speculative + file-backed) pages over the total. The basis is deliberately conservative: inactive
// anonymous pages (they may be waiting on the compressor) and purgeable pages (already inside the inactive and
// anonymous counts) are not counted, so the governor admits less concurrency under memory pressure, never more.
// Missing counters are an error.
func ParseDarwinMemAvailFrac(vmStat []byte, totalBytes uint64) (float64, error) {
	if totalBytes == 0 {
		return 0, errors.New("doomsday governor: zero hw.memsize")
	}
	lines := strings.Split(string(vmStat), "\n")
	var page uint64
	if _, rest, ok := strings.Cut(lines[0], "page size of "); ok {
		if f := strings.Fields(rest); len(f) > 0 {
			page, _ = strconv.ParseUint(f[0], 10, 64)
		}
	}
	if page == 0 || page > 1<<20 {
		return 0, errors.New("doomsday governor: vm_stat has no sane page size")
	}
	want := map[string]bool{"Pages free": false, "Pages speculative": false, "File-backed pages": false}
	var pages uint64
	for _, line := range lines[1:] {
		key, rest, ok := strings.Cut(line, ":")
		if _, w := want[key]; !ok || !w {
			continue
		}
		v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), "."), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("doomsday governor: bad vm_stat %s %q", key, rest)
		}
		want[key] = true
		pages += v
	}
	for k, seen := range want {
		if !seen {
			return 0, fmt.Errorf("doomsday governor: vm_stat lacks %q", k)
		}
	}
	avail := pages * page
	if avail > totalBytes {
		return 0, errors.New("doomsday governor: vm_stat reports more memory than hw.memsize")
	}
	return float64(avail) / float64(totalBytes), nil
}
