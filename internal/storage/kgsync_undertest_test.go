package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

// Saving a person syncs it to the knowledge graph through monomind in the
// background. In a test that never starts the user's real monomind (which
// updates itself over the network and outlives the test): discovery skips an
// install outside the temp dirs and the test home. The fake here stands
// where a real install lives; the same fake in the temp dir does run, so the
// check can fail.
func TestUpsertPersonNeverStartsARealMonomind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake monomind is a shell script")
	}
	monomind.ResetCapabilityCache()
	t.Cleanup(monomind.ResetCapabilityCache)
	db := newUpsertPersonTestDB(t)
	base := t.TempDir()
	tmp, home := filepath.Join(base, "tmp"), filepath.Join(base, "home")
	for _, d := range []string{tmp, home} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("GOTMPDIR", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(monomind.EnvOverride, "")

	fake := func(dir string) (ran string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ran = filepath.Join(dir, "ran")
		script := "#!/bin/sh\necho \"$*\" >> '" + ran + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "monomind"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		return ran
	}
	save := func(name string) {
		t.Helper()
		if err := db.UpsertPerson(&Person{PlatformUsername: name, Platform: "email", ProfileID: "default", FullName: name}); err != nil {
			t.Fatal(err)
		}
		db.kg.wg.Wait()
	}

	ranReal := fake(filepath.Join(base, "real", "bin"))
	save("ada@example.com")
	if b, err := os.ReadFile(ranReal); err == nil {
		t.Fatalf("saving a person started the monomind outside the temp dirs: %s", b)
	}

	ranTemp := fake(filepath.Join(tmp, "bin"))
	save("bob@example.com")
	if _, err := os.ReadFile(ranTemp); err != nil {
		t.Fatalf("the monomind a test put in the temp dir did not run: %v", err)
	}
}
