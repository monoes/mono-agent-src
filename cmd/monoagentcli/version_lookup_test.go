package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// An unstamped build reports "dev" without running git: the lookup used to run
// `git describe` in the inherited cwd (steerable by that repository's local config,
// unbounded in time and output).
func TestGetVersionRunsNoGit(t *testing.T) {
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\necho ran > '" + marker + "'\necho v9.9.9\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	oldVersion := version
	t.Cleanup(func() { version = oldVersion; versionOnce = sync.Once{} })
	version, versionOnce = "", sync.Once{}

	if got := getVersion(); got != "dev" {
		t.Fatalf("getVersion() = %q, want dev", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("getVersion ran git")
	}
}

func TestGetVersionKeepsStampedVersion(t *testing.T) {
	oldVersion := version
	t.Cleanup(func() { version = oldVersion; versionOnce = sync.Once{} })
	version, versionOnce = "v1.2.3", sync.Once{}
	if got := getVersion(); got != "v1.2.3" {
		t.Fatalf("getVersion() = %q", got)
	}
}
