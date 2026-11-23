//go:build !windows

package monomind

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/testhome"
)

// recorder writes an executable that records Claude Code's session marker and its cwd to out.
func recorder(t *testing.T, dir, name, out string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\necho \"marker=${CLAUDECODE-unset} cwd=$(pwd -P)\" > " + out + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readRecord(t *testing.T, out string) string {
	t.Helper()
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the child did not run: %v", err)
	}
	return string(b)
}

// The claude registration turn is a real model turn: it must not inherit the session markers (R08-07).
func TestRegisterClaudeCodeProjectDropsSessionMarkers(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	bins, root, out := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "rec")
	recorder(t, bins, "claude", out)
	t.Setenv("PATH", bins+string(os.PathListSeparator)+os.Getenv("PATH"))
	registerClaudeCodeProject(context.Background(), root, nil, func(string) {})
	if got := readRecord(t, out); !strings.Contains(got, "marker=unset") {
		t.Errorf("claude inherited the session marker: %s", got)
	}
}

// `monomind doctor --fix` must not inherit the session markers (R08-08).
func TestDoctorDropsSessionMarkers(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	bins, dir, out := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "rec")
	bin := recorder(t, bins, "monomind", out)
	_, _ = Doctor(context.Background(), bin, DoctorOptions{Dir: dir, Fix: true})
	if got := readRecord(t, out); !strings.Contains(got, "marker=unset") {
		t.Errorf("doctor inherited the session marker: %s", got)
	}
}

// The agy and codex model probes run with the filtered env, from the neutral dir, and never
// run a binary that sits in the caller's working directory (R08-08).
func TestModelProbesAreScrubbedAndNeutral(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	home := t.TempDir()
	testhome.SetHome(t, home)
	realHome, _ := filepath.EvalSymlinks(home)
	probes := map[string]func(context.Context, string) ([]RuntimeModel, error){"agy": listAntigravityModels, "codex": listCodexModels}
	for name, probe := range probes {
		bins, out := t.TempDir(), filepath.Join(t.TempDir(), "rec")
		bin := recorder(t, bins, name, out)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, _ = probe(ctx, bin)
		cancel()
		got := readRecord(t, out)
		if !strings.Contains(got, "marker=unset") {
			t.Errorf("%s probe inherited the session marker: %s", name, got)
		}
		if !strings.Contains(got, "cwd="+realHome) {
			t.Errorf("%s probe did not run in the neutral dir %s: %s", name, realHome, got)
		}
		// A binary inside the caller's cwd is the project's to swap.
		out2 := filepath.Join(t.TempDir(), "rec2")
		local := t.TempDir()
		bin2 := recorder(t, local, name, out2)
		t.Chdir(local)
		if _, err := probe(context.Background(), bin2); err == nil {
			t.Errorf("%s probe ran a binary from the working directory", name)
		}
		if _, err := os.Stat(out2); err == nil {
			t.Errorf("%s probe executed a binary from the working directory", name)
		}
	}
}
