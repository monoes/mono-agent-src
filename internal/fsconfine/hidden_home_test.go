package fsconfine

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"testing"
)

// A run of a workflow the user never approved neither reads nor writes a
// hidden file or folder of the user's home outside the project folder (keys
// and tokens live there, and files that run code later), writes no git hook
// or git config (in the project too) and nothing into a folder of the home
// on PATH. Elsewhere it reads and writes as before, and an approved run is
// unchanged.
func TestUnapprovedRunsKeepOutOfHiddenHomeFiles(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	project := filepath.Join(home, ".monoagent", "profiles", "p1")
	for _, d := range []string{filepath.Join(home, "notes"), filepath.Join(home, ".config"), filepath.Join(home, "tools"), project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testhome.SetHome(t, home)
	t.Setenv("PATH", filepath.Join(home, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.Symlink(filepath.Join(home, ".config"), filepath.Join(home, "notes", "cfg")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(home)
	guarded := GuardHiddenHome(context.Background(), Guard{Project: project})
	approved := context.Background()

	refusedBoth := []string{
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".config", "autostart", "x.desktop"),
		filepath.Join(home, ".monoagent", "monoagent.db"),
		filepath.Join(home, "notes", "cfg", "systemd", "user", "x.service"), // through a link
		".profile", // relative to the working directory, home here
	}
	refusedWrites := []string{
		filepath.Join(project, ".git", "hooks", "pre-commit"),
		filepath.Join(project, "repo", ".git", "config"),
		filepath.Join(home, "bin", "ls"),                          // ~/bin, which a login shell puts on PATH
		filepath.Join(home, "tools", "git"),                       // on this process's PATH
		filepath.Join(base, "srv", ".git", "hooks", "post-merge"), // a git hook outside the home
	}
	open := []string{
		filepath.Join(home, "notes", "out.csv"),
		filepath.Join(home, "new", "folder", "out.csv"),
		filepath.Join(project, ".monomind", "orgs", "x.json"), // hidden, but in the project folder
		filepath.Join(project, ".monoagent", "vault", "img-001.png"),
		filepath.Join(project, ".gitignore"),
		filepath.Join(home, "tools", "sub", "x.txt"), // below a PATH folder, not on PATH
		filepath.Join(base, "elsewhere", ".hidden"),  // outside the home
	}
	for _, p := range refusedBoth {
		if _, err := Path(guarded, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("unapproved read of %s: err = %v, want the refusal", p, err)
		}
		if _, err := WritePath(guarded, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("unapproved write of %s: err = %v, want the refusal", p, err)
		}
	}
	for _, p := range refusedWrites {
		if _, err := WritePath(guarded, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("unapproved write of %s: err = %v, want the refusal", p, err)
		}
		if _, err := Path(guarded, p); err != nil {
			t.Errorf("unapproved read of %s: %v", p, err)
		}
	}
	for _, p := range append(open, refusedWrites[:2]...) {
		if _, err := Path(guarded, p); err != nil {
			t.Errorf("unapproved read of %s: %v", p, err)
		}
	}
	for _, p := range open {
		if _, err := WritePath(guarded, p); err != nil {
			t.Errorf("unapproved write of %s: %v", p, err)
		}
	}
	for _, p := range append(append(refusedBoth, refusedWrites...), open...) {
		if _, err := Path(approved, p); err != nil {
			t.Errorf("approved read of %s: %v", p, err)
		}
		if _, err := WritePath(approved, p); err != nil {
			t.Errorf("approved write of %s: %v", p, err)
		}
	}
}
