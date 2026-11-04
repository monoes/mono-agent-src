package machine

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

const meminfo = "MemTotal:       16000000 kB\nMemFree:         100000 kB\nMemAvailable:    4000000 kB\nBuffers: 1 kB\n"

func fsys(files map[string]string) func(string, int64) ([]byte, error) {
	return func(p string, max int64) ([]byte, error) {
		s, ok := files[p]
		if !ok {
			return nil, errors.New("no such file")
		}
		if int64(len(s)) > max {
			return nil, errors.New("too large")
		}
		return []byte(s), nil
	}
}

func reader(files map[string]string, st map[string][2]uint64) *Reader {
	return &Reader{
		ReadFile: fsys(files),
		Statfs: func(d string) (uint64, uint64, error) {
			v, ok := st[d]
			if !ok {
				return 0, 0, errors.New("statfs failed")
			}
			return v[0], v[1], nil
		},
		Cores: func() int { return 4 },
		Dirs:  []string{"/data", "/scratch"},
	}
}

func goodFiles() map[string]string {
	return map[string]string{"/proc/loadavg": "1.50 1.00 0.50 2/300 12345\n", "/proc/meminfo": meminfo}
}

func TestSampleReadsAllFields(t *testing.T) {
	r := reader(goodFiles(), map[string][2]uint64{"/data": {50 << 30, 100 << 30}, "/scratch": {20 << 30, 200 << 30}})
	s, err := r.Sample(context.Background())
	if err != nil || !s.Valid {
		t.Fatalf("err=%v valid=%v", err, s.Valid)
	}
	if s.Load1 != 1.5 || s.Cores != 4 || s.MemAvailFrac != 0.25 {
		t.Fatalf("bad sample %+v", s)
	}
	// the SMALLER free space of the two directories, bytes and fraction taken independently
	if s.DiskFreeBytes != 20<<30 || s.DiskFreeFrac != 0.1 {
		t.Fatalf("disk %d %v", s.DiskFreeBytes, s.DiskFreeFrac)
	}
}

func TestSampleFailuresNeverMakeUpAHealthyValue(t *testing.T) {
	okFs := map[string][2]uint64{"/data": {50 << 30, 100 << 30}, "/scratch": {50 << 30, 100 << 30}}
	cases := map[string]func(*Reader, map[string]string){
		"missing loadavg":   func(r *Reader, f map[string]string) { delete(f, "/proc/loadavg") },
		"bad loadavg":       func(r *Reader, f map[string]string) { f["/proc/loadavg"] = "NaN 1 1" },
		"negative load":     func(r *Reader, f map[string]string) { f["/proc/loadavg"] = "-1.0 1 1" },
		"huge load":         func(r *Reader, f map[string]string) { f["/proc/loadavg"] = "1e12 1 1" },
		"no MemAvailable":   func(r *Reader, f map[string]string) { f["/proc/meminfo"] = "MemTotal: 100 kB\n" },
		"avail above total": func(r *Reader, f map[string]string) { f["/proc/meminfo"] = "MemTotal: 100 kB\nMemAvailable: 200 kB\n" },
		"zero total":        func(r *Reader, f map[string]string) { f["/proc/meminfo"] = "MemTotal: 0 kB\nMemAvailable: 0 kB\n" },
		"oversized file":    func(r *Reader, f map[string]string) { f["/proc/meminfo"] = strings.Repeat("x", MaxProcRead+1) },
		"statfs error":      func(r *Reader, f map[string]string) { r.Dirs = []string{"/data", "/missing"} },
		"statfs zero total": func(r *Reader, f map[string]string) {
			r.Statfs = func(string) (uint64, uint64, error) { return 0, 0, nil }
		},
		"statfs free>total": func(r *Reader, f map[string]string) {
			r.Statfs = func(string) (uint64, uint64, error) { return 9, 1, nil }
		},
		"no dirs":       func(r *Reader, f map[string]string) { r.Dirs = nil },
		"zero cores":    func(r *Reader, f map[string]string) { r.Cores = func() int { return 0 } },
		"absurd cores":  func(r *Reader, f map[string]string) { r.Cores = func() int { return 1 << 30 } },
		"no primitives": func(r *Reader, f map[string]string) { r.ReadFile = nil },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			files := goodFiles()
			r := reader(files, okFs)
			mut(r, files)
			s, err := r.Sample(context.Background())
			if err == nil || s.Valid || s != (governor.Sample{}) {
				t.Fatalf("want error and zero sample, got %+v err=%v", s, err)
			}
		})
	}
}

func TestParseLoadAvgRejects(t *testing.T) {
	for _, in := range []string{"", "x", "NaN", "Inf", "-0.1", "1e9"} {
		if _, err := ParseLoadAvg([]byte(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
	if v, err := ParseLoadAvg([]byte("0.00 0 0")); err != nil || v != 0 {
		t.Fatal(v, err)
	}
}

func TestParseMemAvailFracBounds(t *testing.T) {
	v, err := ParseMemAvailFrac([]byte("MemTotal: 10 kB\nMemAvailable: 10 kB\n"))
	if err != nil || v != 1 || math.IsNaN(v) {
		t.Fatal(v, err)
	}
	if _, err := ParseMemAvailFrac([]byte("MemTotal: x kB\nMemAvailable: 1 kB\n")); err == nil {
		t.Fatal("bad number accepted")
	}
	if _, err := ParseMemAvailFrac([]byte("MemTotal:\nMemAvailable: 1 kB\n")); err == nil {
		t.Fatal("empty value accepted")
	}
}

func TestReadsAreBounded(t *testing.T) {
	var asked []int64
	r := reader(goodFiles(), map[string][2]uint64{"/data": {1, 2}, "/scratch": {1, 2}})
	inner := r.ReadFile
	r.ReadFile = func(p string, max int64) ([]byte, error) { asked = append(asked, max); return inner(p, max) }
	if _, err := r.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatal(asked)
	}
	for _, m := range asked {
		if m != MaxProcRead {
			t.Fatalf("read bound %d", m)
		}
	}
}

func TestHungStatfsDoesNotHangTheCallerAndReadingsDoNotPileUp(t *testing.T) {
	release := make(chan struct{})
	r := reader(goodFiles(), nil)
	calls := 0
	r.Statfs = func(string) (uint64, uint64, error) { calls++; <-release; return 1, 2, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Sample(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("caller was held by the hung read")
	}
	if _, err := r.Sample(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("second reading while the first hangs: %v", err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := r.Sample(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reader never recovered")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPanicInAPrimitiveIsAnError(t *testing.T) {
	r := reader(goodFiles(), nil)
	r.Statfs = func(string) (uint64, uint64, error) { panic("boom") }
	s, err := r.Sample(context.Background())
	if err == nil || s.Valid {
		t.Fatalf("%+v %v", s, err)
	}
	// the reader is usable afterwards
	r.Statfs = func(string) (uint64, uint64, error) { return 1, 2, nil }
	if _, err := r.Sample(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledContextReadsNothing(t *testing.T) {
	r := reader(goodFiles(), nil)
	r.ReadFile = func(string, int64) ([]byte, error) { t.Fatal("read happened"); return nil, nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Sample(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFakeScriptsAndRepeatsLast(t *testing.T) {
	f := NewFake(Step{Sample: Healthy()}, Step{Err: errors.New("x")})
	if s, err := f.Sample(context.Background()); err != nil || !s.Valid {
		t.Fatal(s, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.Sample(context.Background()); err == nil {
			t.Fatal("last step not repeated")
		}
	}
	if f.Calls() != 4 {
		t.Fatal(f.Calls())
	}
	if _, err := NewFake().Sample(context.Background()); !errors.Is(err, governor.ErrUnsupported) {
		t.Fatal(err)
	}
	h := NewFake(Step{Hang: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := h.Sample(ctx); err == nil {
		t.Fatal("hang returned nil")
	}
}
