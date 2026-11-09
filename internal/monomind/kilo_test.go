package monomind

import (
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
)

func TestKiloExecutablePin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "kilo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	testhome.SetHome(t, t.TempDir())
	for _, tc := range []struct {
		name, override, searchPath, want string
		shims                            bool
	}{
		{"discovered", "", dir, bin, false},
		{"absolute override", bin, t.TempDir(), bin, false},
		{"relative override", "kilo", dir, bin, false},
		{"missing", filepath.Join(dir, "missing"), dir, "", false},
		{"missing with shims", filepath.Join(dir, "missing"), dir, unpinnedPath("kilo"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetRuntimePins()
			t.Cleanup(resetRuntimePins)
			env := pinRuntimes([]string{"KILO_CLI_BIN=" + tc.override, "KEEP=value"}, tc.searchPath, tc.shims)
			got, present := envValue(env, "KILO_CLI_BIN")
			if got != tc.want || present != (tc.want != "") {
				t.Fatalf("pin = %q (present %v), want %q", got, present, tc.want)
			}
			if got, _ := envValue(env, "KEEP"); got != "value" {
				t.Fatalf("lost unrelated environment: %v", env)
			}
		})
	}
}
