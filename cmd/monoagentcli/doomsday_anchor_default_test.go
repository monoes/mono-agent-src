//go:build !devaccount && !e2estub

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file-store seam for the witness anchor exists only in a devaccount build: a normal build must carry no handling of
// its environment variable, and no wiring hook at all.
func TestDefaultBuildHasNoAnchorFileSeam(t *testing.T) {
	t.Setenv("MONOAGENT_DEV_ANCHOR_FILE_STORE", "1")
	if doomsdayWiringHook != nil {
		t.Fatal("a default build installs a doomsday wiring hook")
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "MONOAGENT_DEV_ANCHOR_FILE_STORE") && !strings.HasPrefix(string(b), "//go:build devaccount") {
			t.Errorf("%s mentions the dev anchor switch without the devaccount build tag", f)
		}
	}
}
