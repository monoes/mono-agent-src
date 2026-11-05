package chat

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/agentbox"
	"github.com/monoes/mono-agent/internal/ai"
)

// The open-ended mode's run directory lives next to the database (<dbdir>/doomsday/<profile>), which is NOT under the
// state directory when --db-path is custom. The box hides the database files by name, so the directory must be named too.

func runDirRig(t *testing.T) (home, db, runDir string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	db = filepath.Join(home, "srv", "data", "agent.db")
	runDir = filepath.Join(filepath.Dir(db), "doomsday", "p1")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "proj"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{db, filepath.Join(runDir, "witness"), filepath.Join(runDir, "generation-counter")} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, db, runDir
}

func TestCoderBoxProtectsTheRunDirectoryOfACustomDatabasePath(t *testing.T) {
	_, db, runDir := runDirRig(t)
	for _, mode := range []string{"compat", "strict"} {
		b, err := coderBox(coderSettings{Box: mode}, ai.AccessCoding, "/w", "claude", "", db)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range b.Protect {
			if p == filepath.Dir(runDir) || p == runDir {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: Protect lacks the run directory (%s): %v", mode, runDir, b.Protect)
		}
	}
}

// The directory is reached through a symlinked database path too, and a database named with ~ gets the absolute directory.
func TestCoderBoxProtectsTheRunDirectoryUnderEveryNameOfTheDatabase(t *testing.T) {
	home, db, runDir := runDirRig(t)
	alias := filepath.Join(home, "alias.db")
	if err := os.Symlink(db, alias); err != nil {
		t.Fatal(err)
	}
	has := func(list []string, want string) bool {
		for _, p := range list {
			if p == want {
				return true
			}
		}
		return false
	}
	b, err := coderBox(coderSettings{Box: "strict"}, ai.AccessCoding, "/w", "claude", "", "~/srv/data/agent.db")
	if err != nil || !has(b.Protect, filepath.Dir(runDir)) {
		t.Fatalf("a tilde path: %v %v", b, err)
	}
	// The mode puts its directory next to the path it was given, the alias.
	b, err = coderBox(coderSettings{Box: "strict"}, ai.AccessCoding, "/w", "claude", "", alias)
	if err != nil {
		t.Fatal(err)
	}
	if !has(b.Protect, filepath.Join(home, "doomsday")) {
		t.Errorf("Protect lacks the run-state directory of the alias: %v", b.Protect)
	}
}

// In the real box the directory cannot be read, listed or removed, and the host's copy is untouched afterwards.
func TestBoxedTurnCannotReachTheRunDirectoryOfACustomDatabasePath(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}
	if out, err := exec.Command("bwrap", "--bind", "/", "/", "true").CombinedOutput(); err != nil {
		t.Skipf("bwrap cannot create namespaces here: %s", out)
	}
	for _, mode := range []string{"compat", "strict"} {
		t.Run(mode, func(t *testing.T) {
			home, db, runDir := runDirRig(t)
			b, err := coderBox(coderSettings{Box: mode}, ai.AccessCoding, filepath.Join(home, "proj"), "", "", db)
			if err != nil {
				t.Fatal(err)
			}
			b.Home = home
			probe := `echo "w=$(cat '` + filepath.Join(runDir, "witness") + `' 2>/dev/null || echo none)"
rm -r -f '` + runDir + `' 2>/dev/null
rm -r -f '` + filepath.Dir(runDir) + `' 2>/dev/null
echo done`
			p, err := b.Wrap("sh", []string{"-c", probe}, os.Environ())
			if agentbox.IsCode(err, agentbox.CodeUntrusted) {
				t.Skipf("bubblewrap is not trusted here: %v", err)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			out, _ := p.Command().CombinedOutput()
			if !strings.Contains(string(out), "w=none") {
				t.Errorf("the witness was readable in the box: %s", out)
			}
			for _, f := range []string{"witness", "generation-counter"} {
				if _, err := os.Stat(filepath.Join(runDir, f)); err != nil {
					t.Errorf("%s was removed from the host by a boxed turn: %v", f, err)
				}
			}
		})
	}
}
