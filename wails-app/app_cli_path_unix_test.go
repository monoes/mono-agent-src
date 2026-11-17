//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIOverrideTrusted(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if !cliOverrideTrusted(mk("ok", 0o755)) {
		t.Error("user-owned 0755 file must be accepted")
	}
	if cliOverrideTrusted(mk("world", 0o757)) {
		t.Error("world-writable file must be refused")
	}
	if cliOverrideTrusted(mk("group", 0o775)) {
		t.Error("group-writable file must be refused")
	}
	if cliOverrideTrusted(filepath.Join(dir, "missing")) {
		t.Error("missing file must be refused")
	}
	if cliOverrideTrusted(dir) {
		t.Error("a directory must be refused")
	}
}

func TestCLIOverrideRefusesWrongOwner(t *testing.T) {
	// /bin/sh is root-owned and, as a normal user, not ours.
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	if cliOverrideTrusted("/bin/sh") {
		t.Error("a file owned by another user must be refused")
	}
}

func TestFindMonoAgentCLIIgnoresWorldWritableOverride(t *testing.T) {
	bad := fakeCLI(t, "exit 0\n")
	if err := os.Chmod(bad, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bad)
	if got, _ := findMonoAgentCLI(); got == bad {
		t.Errorf("world-writable override %s was used", bad)
	}
}
