package chat

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/monomind"
)

func ownRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// TestInitOwnFolderKeepsGitClean: setting up a folder the user picked writes
// monomind's project files, and every file monoagent added is kept out of
// `git status` through .git/info/exclude (not the user's tracked .gitignore),
// and the user is told (#1755).
func TestInitOwnFolderKeepsGitClean(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	ownRun(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin\n"), 0o644)
	ownRun(t, dir, "add", "-A")
	ownRun(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one")

	prev := initWorkspace
	t.Cleanup(func() { initWorkspace = prev })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		os.MkdirAll(filepath.Join(d, ".claude", "helpers"), 0o755)
		os.WriteFile(filepath.Join(d, ".claude", "helpers", "x.cjs"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(d, ".mcp.json"), []byte("{}"), 0o644)
		os.WriteFile(filepath.Join(d, "CLAUDE.md"), []byte("#"), 0o644)
		return &monomind.WorkspaceInit{Root: d}, nil
	}
	var notice bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &notice, dir, "claude"); err != nil {
		t.Fatal(err)
	}
	if st := strings.TrimSpace(ownRun(t, dir, "status", "--porcelain")); st != "" {
		t.Errorf("git status is not clean after init:\n%s", st)
	}
	for _, want := range []string{".claude", ".mcp.json", "CLAUDE.md"} {
		if !strings.Contains(notice.String(), want) {
			t.Errorf("notice does not name %s:\n%s", want, notice.String())
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(b) != "bin\n" {
		t.Errorf("the user's .gitignore was edited: %q", b)
	}
	// A file the user already had is never excluded.
	if strings.Contains(ownRun(t, dir, "ls-files"), "main.go") == false {
		t.Errorf("main.go lost")
	}
	if ex, _ := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude")); strings.Contains(string(ex), "main.go") {
		t.Errorf("a pre-existing file was excluded:\n%s", ex)
	}
}

// TestInitOwnFolderNotARepo: a folder that is not a git work tree is set up
// as before, with the notice and no error.
func TestInitOwnFolderNotARepo(t *testing.T) {
	dir := t.TempDir()
	prev := initWorkspace
	t.Cleanup(func() { initWorkspace = prev })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		os.WriteFile(filepath.Join(d, "CLAUDE.md"), []byte("#"), 0o644)
		return &monomind.WorkspaceInit{Root: d}, nil
	}
	var notice bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &notice, dir, "claude"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice.String(), "CLAUDE.md") {
		t.Errorf("notice does not name CLAUDE.md:\n%s", notice.String())
	}
}
