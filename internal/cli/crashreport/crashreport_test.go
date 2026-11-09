package crashreport

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestReportCrashLocalOnlyDefault verifies the default behavior: with the
// crash-report opt-out env unset and no `monomind` resolvable on PATH, a
// crash is recorded under $HOME/.monoagent/crashes/crash-<unixts>.log and
// no subprocess is ever consulted (the env gate is checked before any
// LookPath, so an empty PATH cannot change the outcome either).
func TestReportCrashLocalOnlyDefault(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_CRASH_REPORT", "")
	// Strip PATH so exec.LookPath could never succeed even if reached.
	t.Setenv("PATH", "")

	before := time.Now().Unix()
	Report("boom test panic", []byte("goroutine 1 [running]:\nfake stack"), Build{Version: "v9.9.9", BuildDate: "2026-10-14T00:00:00Z", Repo: "owner/repo"})

	crashDir := filepath.Join(home, ".monoagent", "crashes")
	entries, err := os.ReadDir(crashDir)
	if err != nil {
		t.Fatalf("crashes dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 crash file, got %d", len(entries))
	}
	name := entries[0].Name()
	if !strings.HasPrefix(name, "crash-") || !strings.HasSuffix(name, ".log") {
		t.Fatalf("crash file name = %q, want crash-<unixts>.log", name)
	}
	ts, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "crash-"), ".log"), 10, 64)
	if err != nil {
		t.Fatalf("crash file name %q does not carry a unix timestamp: %v", name, err)
	}
	if ts < before-2 || ts > time.Now().Unix()+2 {
		t.Errorf("crash timestamp %d outside expected window [%d, %d]", ts, before-2, time.Now().Unix()+2)
	}

	content, err := os.ReadFile(filepath.Join(crashDir, name))
	if err != nil {
		t.Fatalf("read crash file: %v", err)
	}
	s := string(content)
	for _, want := range []string{"boom test panic", "goroutine 1 [running]", "timestamp:", "version: v9.9.9 (built 2026-10-14T00:00:00Z)", "Version: v9.9.9"} {
		if !strings.Contains(s, want) {
			t.Errorf("crash record missing %q:\n%s", want, s)
		}
	}
}

// TestReportCrashLocalWhenNotOptedIn pins the ordering of the gate: even
// with a `monomind` shim on PATH, the default (no MONOAGENT_CRASH_REPORT)
// must stay local — the env var, not the binary's presence, is the switch.
func TestReportCrashLocalWhenNotOptedIn(t *testing.T) {
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_CRASH_REPORT", "")

	binDir := t.TempDir()
	shim := filepath.Join(binDir, "monomind")
	probe := filepath.Join(home, "probe")
	script := "#!/bin/sh\necho touched > " + probe + "\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatalf("write monomind shim: %v", err)
	}
	t.Setenv("PATH", binDir)

	Report("local only", []byte("stack"), Build{Version: "v9.9.9", Repo: "owner/repo"})

	if _, err := os.Stat(probe); err == nil {
		t.Fatal("monomind was invoked without MONOAGENT_CRASH_REPORT=1")
	}
	if _, err := os.Stat(filepath.Join(home, ".monoagent", "crashes")); err != nil {
		t.Fatalf("local crash record missing: %v", err)
	}
}
