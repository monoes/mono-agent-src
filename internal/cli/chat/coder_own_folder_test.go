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

// A folder below the work tree's top is excluded by its own path, and an
// existing exclude file with no trailing newline keeps its last pattern intact.
func TestInitOwnFolderBelowTheTopAndExistingExclude(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	top := t.TempDir()
	ownRun(t, top, "init", "-q")
	sub := filepath.Join(top, "sub")
	os.MkdirAll(sub, 0o755)
	os.WriteFile(filepath.Join(sub, "main.go"), []byte("package x\n"), 0o644)
	ownRun(t, top, "add", "-A")
	ownRun(t, top, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one")
	os.MkdirAll(filepath.Join(top, ".git", "info"), 0o755)
	os.WriteFile(filepath.Join(top, ".git", "info", "exclude"), []byte("node_modules"), 0o644) // no trailing newline
	os.WriteFile(filepath.Join(top, ".claude"), []byte("a user's own file at the top"), 0o644)

	prev := initWorkspace
	t.Cleanup(func() { initWorkspace = prev })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		os.MkdirAll(filepath.Join(d, ".claude"), 0o755)
		os.WriteFile(filepath.Join(d, ".claude", "x"), []byte("x"), 0o644)
		return &monomind.WorkspaceInit{Root: d}, nil
	}
	if _, err := initOwnFolder(context.Background(), &bytes.Buffer{}, sub, "claude"); err != nil {
		t.Fatal(err)
	}
	ex, _ := os.ReadFile(filepath.Join(top, ".git", "info", "exclude"))
	if !strings.Contains(string(ex), "node_modules\n") || strings.Contains(string(ex), "node_modules#") {
		t.Errorf("the user's last pattern was glued to our comment:\n%q", ex)
	}
	if !strings.Contains(string(ex), "/sub/.claude") || strings.Contains(string(ex), "\n/.claude") {
		t.Errorf("the pattern is not anchored at sub/:\n%s", ex)
	}
	if st := ownRun(t, top, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, "sub/.claude") {
		t.Errorf("sub/.claude shows in git status:\n%s", st)
	}
	if st := ownRun(t, top, "status", "--porcelain"); !strings.Contains(st, ".claude") {
		t.Errorf("the user's own top-level .claude is hidden:\n%s", st)
	}
}

// fakeSetup stands in for monomind init: it writes files (and appends ignore
// rules) and reports as created only what is listed in created.
func fakeSetup(t *testing.T, files []string, ignore string, created []string) {
	t.Helper()
	prev := initWorkspace
	t.Cleanup(func() { initWorkspace = prev })
	initWorkspace = func(_ context.Context, _, d, _ string) (*monomind.WorkspaceInit, error) {
		for _, f := range files {
			p := filepath.Join(d, filepath.FromSlash(f))
			os.MkdirAll(filepath.Dir(p), 0o755)
			if _, err := os.Stat(p); err != nil {
				os.WriteFile(p, []byte("setup"), 0o644)
			}
		}
		if p := filepath.Join(d, ".gitignore"); ignore != "" {
			b, _ := os.ReadFile(p)
			if !strings.Contains(string(b), ignore) {
				f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
				f.WriteString(ignore)
				f.Close()
			}
		}
		return &monomind.WorkspaceInit{Root: d, Created: created}, nil
	}
}

// A repository that already has .claude/ and a .gitignore: what the setup adds
// inside is kept out of git status, its new ignore rules leave the user's file
// alone, and a file the setup does not report stays visible (#1755).
func TestInitOwnFolderExistingFoldersAndGitignore(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	for _, sub := range []string{"", "app/[id]"} {
		t.Run("at "+sub, func(t *testing.T) {
			top := t.TempDir()
			dir := filepath.Join(top, filepath.FromSlash(sub))
			ownRun(t, top, "init", "-q")
			os.MkdirAll(filepath.Join(dir, ".claude"), 0o755)
			os.WriteFile(filepath.Join(dir, ".claude", "mine.json"), []byte("{}"), 0o644)
			os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin\n"), 0o644)
			ownRun(t, top, "add", "-A")
			ownRun(t, top, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one")

			// the last file stands for one another process wrote during the setup
			fakeSetup(t, []string{".claude/helpers/a b.cjs", ".claude/settings.json", ".claude/memory.db", "scratch/state.json", ".claude/users-new.md"},
				"\n# monomind runtime state\ndata/sessions/\n.swarm/\n.claude/memory.db\n",
				[]string{".claude/helpers", ".claude/settings.json", ".claude/memory.db", "scratch", "memory: 1 pattern seeded"})
			var notice bytes.Buffer
			for range 2 { // a second chat on the same folder
				if _, err := initOwnFolder(context.Background(), &notice, dir, "claude"); err != nil {
					t.Fatal(err)
				}
			}
			prefix := ""
			if sub != "" {
				prefix = sub + "/"
			}
			want := "?? " + prefix + ".claude/users-new.md"
			if st := strings.ReplaceAll(strings.TrimSpace(ownRun(t, top, "status", "--porcelain", "--untracked-files=all")), `"`, ""); st != want {
				t.Errorf("git status = %q, want only %q", st, want)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(b) != "bin\n" {
				t.Errorf(".gitignore = %q, want it as it was", b)
			}
			// the moved rules still ignore what they ignored, at any depth for a bare name
			for _, f := range []string{"deep/.swarm/db", "data/sessions/s"} {
				p := filepath.Join(dir, filepath.FromSlash(f))
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte("x"), 0o644)
			}
			if st := ownRun(t, top, "status", "--porcelain", "--untracked-files=all"); strings.Contains(st, "swarm/") || strings.Contains(st, "data/sessions") {
				t.Errorf("a moved ignore rule no longer applies:\n%s", st)
			}
			ex, _ := os.ReadFile(filepath.Join(top, ".git", "info", "exclude"))
			if n := strings.Count(string(ex), "data/sessions/"); n != 1 {
				t.Errorf("the rule is listed %d times:\n%s", n, ex)
			}
			for _, want := range []string{"2 new file(s) inside .claude", ".gitignore is as it was", "scratch"} {
				if !strings.Contains(notice.String(), want) {
					t.Errorf("notice lacks %q:\n%s", want, notice.String())
				}
			}
		})
	}
}

// Outside a git work tree there is nowhere to move ignore rules to: they stay
// and the user is told.
func TestInitOwnFolderGitignoreOutsideARepo(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("bin\n"), 0o644)
	fakeSetup(t, nil, "\n# monomind runtime state\ndata/sessions/\n", nil)
	var notice bytes.Buffer
	if _, err := initOwnFolder(context.Background(), &notice, dir, "claude"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(b) != "bin\n\n# monomind runtime state\ndata/sessions/\n" {
		t.Errorf(".gitignore = %q", b)
	}
	if !strings.Contains(notice.String(), "ignore rules at the end of .gitignore") {
		t.Errorf("notice: %q", notice.String())
	}
}
