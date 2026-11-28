package chat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

const testBlock = "\n# monomind runtime state\n.monomind/sessions/\n.swarm/\n"

// gitignoreSetup runs initOwnFolder in a fresh repository whose .gitignore
// holds was, with a setup during which the file ends up holding became.
func gitignoreSetup(t *testing.T, was, became string) (dir, notice string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir = t.TempDir()
	ownRun(t, dir, "init", "-q")
	p := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(p, []byte(was), 0o600); err != nil {
		t.Fatal(err)
	}
	prev := initWorkspace
	t.Cleanup(func() { initWorkspace = prev })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		os.WriteFile(p, []byte(became), 0o600)
		return &monomind.WorkspaceInit{Root: d}, nil
	}
	var out bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &out, dir, "claude"); err != nil {
		t.Fatal(err)
	}
	return dir, out.String()
}

// Only exactly the setup's block is taken back. Anything else in the file's
// change may be the user's: the file is left as it is, and they are told.
func TestKeepGitignoreLeavesAnythingElseAlone(t *testing.T) {
	for name, c := range map[string]struct{ was, became string }{
		"lines added after the block":    {"bin\n", "bin\n" + testBlock + "secrets.env\nbuild/\n"},
		"a blank line after the block":   {"bin\n", "bin\n" + testBlock + "\n"},
		"a comment after the block":      {"bin\n", "bin\n" + testBlock + "# mine\n"},
		"a rule of the setup's repeated": {"bin\n", "bin\n" + testBlock + ".swarm/\n"},
		"a rule out of order":            {"bin\n", "bin\n" + testBlock + ".monomind/data/\n"},
		"a rule the file already had":    {".swarm/\n", ".swarm/\n" + testBlock},
		"no final newline":               {"bin\n", "bin\n\n# monomind runtime state\n.swarm/"},
		"lines added before the block":   {"bin\n", "bin\nsecrets.env\n" + testBlock},
		"the old content edited":         {"bin\n", "out\n" + testBlock},
		"only the user's own lines":      {"bin\n", "bin\n# monomind notes\nsecrets.env\n"},
	} {
		t.Run(name, func(t *testing.T) {
			dir, notice := gitignoreSetup(t, c.was, c.became)
			if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(b) != c.became {
				t.Errorf(".gitignore = %q, want it left as %q", b, c.became)
			}
			if !strings.Contains(notice, "left as it is") || strings.Contains(notice, "as it was)") {
				t.Errorf("notice: %q", notice)
			}
			if ex, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude")); strings.Contains(string(ex), "secrets.env") {
				t.Errorf("the user's lines were copied to info/exclude:\n%s", ex)
			}
		})
	}
}

func TestKeepGitignoreTakesBackExactlyTheBlock(t *testing.T) {
	for _, was := range []string{"", "bin\n", "bin"} {
		sep := ""
		if was == "bin" {
			sep = "\n"
		}
		dir, notice := gitignoreSetup(t, was, was+sep+testBlock)
		p := filepath.Join(dir, ".gitignore")
		if b, _ := os.ReadFile(p); string(b) != was {
			t.Errorf(".gitignore = %q, want %q", b, was)
		}
		if st, _ := os.Stat(p); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v", st.Mode())
		}
		if !strings.Contains(notice, "your .gitignore is as it was") {
			t.Errorf("notice: %q", notice)
		}
	}
}

// A .gitignore that is a link is never written through.
func TestKeepGitignoreDoesNotFollowALink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	ownRun(t, dir, "init", "-q")
	target := filepath.Join(t.TempDir(), "shared-ignore")
	os.WriteFile(target, []byte("bin\n"), 0o644)
	if err := os.Symlink(target, filepath.Join(dir, ".gitignore")); err != nil {
		t.Skip("no symlinks")
	}
	fakeSetup(t, nil, testBlock, nil)
	var notice bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &notice, dir, "claude"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "bin\n"+testBlock {
		t.Errorf("the link's target was rewritten: %q", b)
	}
	if !strings.Contains(notice.String(), "not a plain file") {
		t.Errorf("notice: %q", notice.String())
	}
	if err := cutBack(filepath.Join(dir, ".gitignore"), []byte("bin\n"+testBlock), 4); err == nil {
		t.Error("cutBack followed a link")
	}
}

func TestCutBackRefusesAFileThatChangedAgain(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	os.WriteFile(p, []byte("old+block+more"), 0o644)
	if err := cutBack(p, []byte("old+block"), 3); err == nil {
		t.Error("a grown file was truncated")
	}
	if b, _ := os.ReadFile(p); string(b) != "old+block+more" {
		t.Errorf("content = %q", b)
	}
}

// Work trees of one repository share info/exclude: the notice says so.
func TestInitOwnFolderNamesTheSharedExclude(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	top := t.TempDir()
	ownRun(t, top, "init", "-q")
	ownRun(t, top, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one", "--allow-empty")
	linked := filepath.Join(t.TempDir(), "wt")
	ownRun(t, top, "worktree", "add", "-q", "--detach", linked)
	fakeSetup(t, []string{"CLAUDE.md"}, "", nil)
	var notice bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &notice, linked, "claude"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.String(), "other work trees share that file") {
		t.Errorf("notice: %q", notice.String())
	}
}
