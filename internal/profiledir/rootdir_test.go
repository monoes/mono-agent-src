package profiledir

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestValidateRootDirRefusesDirsThatWidenTheRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	bad := map[string]string{
		"empty":            "",
		"relative":         "rel/dir",
		"filesystem root":  string(filepath.Separator),
		"home":             home,
		"ancestor of home": filepath.Dir(home),
		"hidden in home":   filepath.Join(home, ".ssh"),
		"under hidden":     filepath.Join(home, ".config", "proj"),
		"not clean":        home + "/proj/../proj2",
	}
	for name, dir := range bad {
		if err := ValidateRootDir(dir); err == nil {
			t.Errorf("%s: ValidateRootDir(%q) accepted it", name, dir)
		}
	}
	for _, dir := range []string{filepath.Join(home, "my-project"), filepath.Join(home, "a", "b")} {
		if err := ValidateRootDir(dir); err != nil {
			t.Errorf("ValidateRootDir(%q) = %v", dir, err)
		}
	}
}

func TestRootFallsBackToTheDefaultForAnInvalidStoredRootDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE profiles (id TEXT PRIMARY KEY, root_dir TEXT)`); err != nil {
		t.Fatal(err)
	}
	for id, dir := range map[string]string{"p1": "/", "p2": home, "p3": filepath.Join(home, ".ssh"), "p4": "relative"} {
		if _, err := db.Exec(`INSERT INTO profiles VALUES (?, ?)`, id, dir); err != nil {
			t.Fatal(err)
		}
		got := Root(db, id)
		if want := filepath.Join(home, ".monoagent", "profiles", id); got != want {
			t.Errorf("Root with root_dir %q = %q, want the default %q", dir, got, want)
		}
	}
	if _, err := db.Exec(`INSERT INTO profiles VALUES ('ok', ?)`, filepath.Join(home, "proj")); err != nil {
		t.Fatal(err)
	}
	if got := Root(db, "ok"); !strings.HasSuffix(got, "proj") {
		t.Errorf("a valid override must still win, got %q", got)
	}
}
