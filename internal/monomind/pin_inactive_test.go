package monomind

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// inactiveFixture: a mise whose `which` fails the way the real one does for
// a bin installed under a node that is not the active one.
type inactiveFixture struct {
	data, shim, outside string
}

func newInactiveFixture(t *testing.T, whichScript string) *inactiveFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binaries are shell scripts")
	}
	f := &inactiveFixture{}
	home := t.TempDir()
	f.data = filepath.Join(home, "mise")
	f.outside = t.TempDir()
	tools := t.TempDir()
	mise := filepath.Join(tools, "mise")
	script := "#!/bin/sh\n" + whichScript
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.data, "shims"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.shim = filepath.Join(f.data, "shims", "monomind")
	if err := os.Symlink(mise, f.shim); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"HOME": home, "MISE_DATA_DIR": f.data, "XDG_DATA_HOME": "", "RTX_DATA_DIR": ""} {
		t.Setenv(k, v)
	}
	return f
}

const notActive = "echo 'mise ERROR monomind is a mise bin however it is not currently active. Use `mise use` to activate it in this directory.' >&2\nexit 1\n"

func (f *inactiveFixture) install(t *testing.T, rel, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(f.data, "installs", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// The user's case: monomind is an npm global under node 26.8.1 while the
// active node is 26.13.0, so `mise which monomind` fails.
func TestInactiveNodeVersionBinResolves(t *testing.T) {
	f := newInactiveFixture(t, notActive)
	want := f.install(t, "node/26.8.1/bin/monomind", "", 0o755)
	if err := os.WriteFile(want, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.install(t, "node/26.8.1/bin/node", "", 0o755)
	f.install(t, "node/26.13.0/bin/other", "", 0o755) // active node, no monomind
	got, err := pinPath(f.shim, "monomind")
	if err != nil || got != want {
		t.Fatalf("pinPath = %q, %v; want %s", got, err, want)
	}
	nd, err := pinNodeDir(got)
	if err != nil || nd != filepath.Dir(want) {
		t.Fatalf("pinNodeDir = %q, %v; want the node next to it, %s", nd, err, filepath.Dir(want))
	}
}

func TestInactiveFallbackPicksHighestVersionHavingTheBin(t *testing.T) {
	f := newInactiveFixture(t, notActive)
	f.install(t, "node/26.8.1/bin/monomind", "", 0o755)
	f.install(t, "node/26.13.0/bin/monomind", "", 0o755)
	f.install(t, "node/26.20.0/bin/other", "", 0o755)
	f.install(t, "node/latest/bin/monomind", "", 0o755)
	want := filepath.Join(f.data, "installs", "node", "26.13.0", "bin", "monomind")
	if got, err := pinPath(f.shim, "monomind"); err != nil || got != want {
		t.Fatalf("pinPath = %q, %v; want %s (26.13.0 > 26.8.1 numerically)", got, err, want)
	}
}

func TestInactiveFallbackRefusals(t *testing.T) {
	f := newInactiveFixture(t, notActive)
	f.install(t, "node/26.20.0/bin/monomind", "", 0o644) // not executable: skipped
	f.install(t, "node/26.8.1/bin/monomind", "", 0o755)
	// A symlink out of the installs dir is never chosen, however high its version.
	planted := filepath.Join(f.outside, "monomind")
	if err := os.WriteFile(planted, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.data, "installs", "node", "30.0.0", "bin", "monomind")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(planted, link); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(f.data, "installs", "node", "26.8.1", "bin", "monomind")
	if got, err := pinPath(f.shim, "monomind"); err != nil || got != want {
		t.Fatalf("pinPath = %q, %v; want %s", got, err, want)
	}
}

func TestInactiveFallbackIgnoresProjectCopy(t *testing.T) {
	f := newInactiveFixture(t, notActive)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "monomind"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	_, err := pinPath(f.shim, "monomind")
	if err == nil {
		t.Fatal("pinPath chose something with nothing installed")
	}
	for _, want := range []string{"not currently active", "Scanning " + filepath.Join(f.data, "installs"), "npm install -g monomind@latest"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q lacks %q", err, want)
		}
	}
}

// When `which` succeeds, its answer is used even if a higher version is installed.
func TestWhichSuccessUnchangedByFallback(t *testing.T) {
	f := newInactiveFixture(t, "")
	active := f.install(t, "node/26.8.1/bin/monomind", "", 0o755)
	f.install(t, "node/26.13.0/bin/monomind", "", 0o755)
	script := "#!/bin/sh\necho '" + active + "'\n"
	mise, _ := filepath.EvalSymlinks(f.shim)
	if err := os.WriteFile(mise, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := pinPath(f.shim, "monomind"); err != nil || got != active {
		t.Fatalf("pinPath = %q, %v; want which's answer %s", got, err, active)
	}
}
