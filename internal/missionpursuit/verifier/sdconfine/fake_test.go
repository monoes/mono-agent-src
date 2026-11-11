package sdconfine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSys is the system the Confiner talks to, scripted: nothing here starts a process.
type fakeSys struct {
	mu      sync.Mutex
	calls   [][]string // bin base name, then its arguments
	handler func(bin string, args []string) runResult
	files   map[string]string
	now     time.Time
	version string
	goos    string
	trust   map[string]bool // names that are NOT trusted
	runtime string
	busErr  error
	rtErr   error
	id      int
	alive   map[int]string // pid -> start time of the processes that are alive
}

const goodProbe = "memory=2147483648\npids=512\ncpu=200000 100000\n"

func newFakeSys() *fakeSys {
	return &fakeSys{goos: "linux", version: "262.1-1-arch", runtime: "/run/user/1000", now: time.Unix(1_800_000_000, 0), files: map[string]string{}, alive: map[int]string{4242: "100", 4243: "7"}}
}

func (f *fakeSys) sys() system {
	return system{
		goos: f.goos,
		uid:  1000,
		pid:  4242,
		procStart: func(pid int) (string, error) {
			if st, ok := f.alive[pid]; ok {
				return st, nil
			}
			return "", os.ErrNotExist
		},
		getenv: func(k string) string {
			return map[string]string{"XDG_RUNTIME_DIR": f.runtime, "HOME": "/home/x", "SECRET": "s3cret"}[k]
		},
		trusted: func(name string) (string, error) {
			if f.trust[name] {
				return "", fmt.Errorf("%s is not trusted", name)
			}
			return "/usr/bin/" + name, nil
		},
		checkRuntimeDir: func(string) error { return f.rtErr },
		checkBus:        func(string) error { return f.busErr },
		readFile: func(p string) ([]byte, error) {
			if s, ok := f.files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		run: func(_ context.Context, bin string, env []string, args ...string) runResult {
			f.mu.Lock()
			f.calls = append(f.calls, append([]string{filepath.Base(bin)}, args...))
			f.mu.Unlock()
			if f.handler != nil {
				if r := f.handler(filepath.Base(bin), args); r.Code != -1 {
					return r
				}
			}
			return f.defaults(filepath.Base(bin), args)
		},
		newID: func() string { f.id++; return fmt.Sprintf("%016x", f.id) },
		now:   func() time.Time { return f.now },
		sleep: f.sleep,
	}
}

func (f *fakeSys) defaults(bin string, args []string) runResult {
	switch {
	case bin == "systemd-run":
		return runResult{Stdout: goodProbe}
	case bin == "systemctl" && has(args, "show") && has(args, "Version"):
		return runResult{Stdout: f.version + "\n"}
	case bin == "systemctl" && has(args, "show"):
		return runResult{Stdout: "LoadState=not-found\nActiveState=inactive\nControlGroup=\n"}
	}
	return runResult{}
}

func has(args []string, s string) bool {
	for _, a := range args {
		if a == s || strings.Contains(a, s) {
			return true
		}
	}
	return false
}

func (f *fakeSys) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

func (f *fakeSys) sleep(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

func (f *fakeSys) reset() { f.mu.Lock(); f.calls = nil; f.mu.Unlock() }

// newConfiner builds a Confiner over the fake system and a real scratch base.
func newConfiner(t *testing.T, f *fakeSys, cfg Config) *Confiner {
	t.Helper()
	if cfg.ScratchBase == "" {
		cfg.ScratchBase = t.TempDir()
	}
	c, err := newWith(context.Background(), cfg, f.sys())
	if err != nil {
		t.Fatal(err)
	}
	f.reset()
	f.id = 0 // the probe used one
	return c
}
