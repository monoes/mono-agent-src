package monomind

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/testhome"
)

// writeFakeMonomind writes an executable monomind script into dir.
func writeFakeMonomind(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "monomind")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	return ra == rb
}

// Under test (testhome marks the binary), discovery finds a monomind a test
// put in a temp dir or the test home, never one elsewhere — where the
// user's real install lives. An explicit MONOMIND_BIN is honoured wherever
// it points, and outside a test nothing changes.
func TestDiscoveryUnderTestFindsOnlyTempInstalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	if UnderTestEnv != testhome.UnderTestEnv || os.Getenv(UnderTestEnv) != "1" {
		t.Fatalf("testhome marks tests with %s=%q; monomind reads %s", testhome.UnderTestEnv, os.Getenv(testhome.UnderTestEnv), UnderTestEnv)
	}
	ResetCapabilityCache()
	t.Cleanup(ResetCapabilityCache)
	// The temp dirs and HOME move into base, so base/real stands where a
	// real install lives: outside all of them.
	base := t.TempDir()
	tmp, home := filepath.Join(base, "tmp"), filepath.Join(base, "home")
	for _, d := range []string{tmp, home} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOTMPDIR", "")
	testhome.SetHome(t, home)
	t.Setenv(EnvOverride, "")
	real := writeFakeMonomind(t, filepath.Join(base, "real", "bin"))
	t.Setenv("PATH", filepath.Dir(real))

	if got, err := Find(); err == nil {
		t.Fatalf("Find() = %s: a monomind outside the temp dirs and the test home was found", got)
	}
	for _, p := range FindAll() {
		if sameFile(t, p, real) {
			t.Fatalf("FindAll() = %v lists the install outside the temp dirs", FindAll())
		}
	}

	for _, dir := range []string{filepath.Join(tmp, "bin"), filepath.Join(home, ".local", "bin")} {
		fake := writeFakeMonomind(t, dir)
		t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+filepath.Dir(real))
		got, err := Find()
		if err != nil || !sameFile(t, got, fake) {
			t.Fatalf("Find() = %q, %v; want the fake in %s", got, err, dir)
		}
		os.Remove(fake)
	}

	// A fake may be a symlink in the temp dir to a script elsewhere
	// (testdata): where it was found is what counts.
	link := filepath.Join(tmp, "link", "monomind")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(link)+string(os.PathListSeparator)+filepath.Dir(real))
	if got, err := Find(); err != nil || !sameFile(t, got, real) {
		t.Fatalf("Find() = %q, %v; want the symlinked fake in the temp dir", got, err)
	}
	os.Remove(link)

	t.Setenv("PATH", filepath.Dir(real))
	t.Setenv(EnvOverride, real)
	if got, err := Find(); err != nil || !sameFile(t, got, real) {
		t.Fatalf("with %s set, Find() = %q, %v; want %s", EnvOverride, got, err, real)
	}

	t.Setenv(EnvOverride, "")
	t.Setenv(UnderTestEnv, "")
	if got, err := Find(); err != nil || !sameFile(t, got, real) {
		t.Fatalf("outside a test, Find() = %q, %v; want %s", got, err, real)
	}
}
