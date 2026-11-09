package fsconfine

import (
	"context"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func guardHome(t *testing.T) (base, home string) {
	t.Helper()
	base = t.TempDir()
	home = filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	testhome.SetHome(t, home)
	return base, home
}

// A refusal says how to approve the workflow, and names a hidden file of the
// home as one, not as lying in itself.
func TestARefusalSaysHowToApprove(t *testing.T) {
	_, home := guardHome(t)
	ctx := GuardHiddenHome(context.Background(), Guard{Approve: "run `monoagentcli --profile p workflow activate wf-1`"})
	_, err := WritePath(ctx, filepath.Join(home, ".bashrc"))
	if !errors.Is(err, ErrUnapprovedPath) || !strings.Contains(err.Error(), "workflow activate wf-1") ||
		!strings.Contains(err.Error(), ".bashrc is one") {
		t.Fatalf("err = %v", err)
	}
}

// MonoAgent's own folders the guard names stay usable: the attachments to
// read, the export folder to read and write.
func TestTheGuardsOwnFoldersStayOpen(t *testing.T) {
	_, home := guardHome(t)
	att, out := filepath.Join(home, ".monoagent", "attachments"), filepath.Join(home, ".monoagent", "output")
	ctx := GuardHiddenHome(context.Background(), Guard{ReadOK: []string{att}, WriteOK: []string{out}})
	if _, err := Path(ctx, filepath.Join(att, "m1", "invoice.pdf")); err != nil {
		t.Errorf("read an attachment: %v", err)
	}
	if _, err := WritePath(ctx, filepath.Join(att, "m1", "x.sh")); !errors.Is(err, ErrUnapprovedPath) {
		t.Errorf("write into the attachments: %v", err)
	}
	for _, f := range []func(context.Context, string) (string, error){Path, WritePath} {
		if _, err := f(ctx, filepath.Join(out, "export.json")); err != nil {
			t.Errorf("the export folder: %v", err)
		}
	}
	if _, err := Path(ctx, filepath.Join(home, ".monoagent", "monoagent.db")); !errors.Is(err, ErrUnapprovedPath) {
		t.Errorf("the database next to them: %v", err)
	}
}

// A project folder that is the home or holds it, or lies in a hidden folder
// of the home other than ~/.monoagent/profiles, is no project folder: the
// hidden files stay closed.
func TestAProjectFolderCantOpenTheHome(t *testing.T) {
	base, home := guardHome(t)
	ssh := filepath.Join(home, ".ssh")
	link := filepath.Join(home, "proj-link")
	for _, d := range []string{ssh, filepath.Join(home, "code", "proj"), filepath.Join(home, ".monoagent", "profiles", "p1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ssh, link); err != nil {
		t.Fatal(err)
	}
	for _, project := range []string{home, base, ssh, link, filepath.Join(home, ".config", "x")} {
		ctx := GuardHiddenHome(context.Background(), Guard{Project: project})
		if _, err := Path(ctx, filepath.Join(ssh, "id_ed25519")); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("project %s opened ~/.ssh: %v", project, err)
		}
	}
	for _, project := range []string{filepath.Join(home, "code", "proj"), filepath.Join(home, ".monoagent", "profiles", "p1")} {
		ctx := GuardHiddenHome(context.Background(), Guard{Project: project})
		if _, err := WritePath(ctx, filepath.Join(project, "notes", "x.md")); err != nil {
			t.Errorf("project %s: %v", project, err)
		}
	}
}

// Git hooks and configs, in every place git keeps them, in any case.
func TestGitRunsIt(t *testing.T) {
	sep := string(filepath.Separator)
	j := func(s string) string { return strings.ReplaceAll(s, "/", sep) }
	for _, p := range []string{
		"/p/.git/hooks/pre-commit", "/p/.git/config", "/p/.git/config.worktree",
		"/p/.git/modules/m/hooks/post-checkout", "/p/.git/modules/m/config",
		"/p/.git/worktrees/w/config.worktree", "/srv/repo.git/hooks/update",
		"/p/.GIT/hooks/x", "/p/.git/HOOKS/x",
	} {
		if !gitRunsIt(j(p)) {
			t.Errorf("gitRunsIt(%s) = false", p)
		}
	}
	for _, p := range []string{"/p/.gitignore", "/p/.git/HEAD", "/p/.git/hooks", "/p/src/config.go", "/p/notes.git"} {
		if gitRunsIt(j(p)) {
			t.Errorf("gitRunsIt(%s) = true", p)
		}
	}
}

// The folders on PATH a package manager or a login shell adds are covered,
// in the home and out of it.
func TestOnPathCoversTheUsualFolders(t *testing.T) {
	_, home := guardHome(t)
	t.Setenv("PATH", "") // the built-in folders alone
	for _, d := range []string{filepath.Join(home, "bin"), filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin")} {
		if !onPath(home, d) {
			t.Errorf("onPath(%s) = false", d)
		}
	}
	if runtime.GOOS != "windows" && !onPath(home, "/usr/local/bin") {
		t.Error("onPath(/usr/local/bin) = false")
	}
	if onPath(home, filepath.Join(home, "notes")) {
		t.Error("onPath(~/notes) = true")
	}
}

// A process's environment, where the daemon's keys can be, stays unread.
func TestNoProcessEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc is Linux's")
	}
	guardHome(t)
	ctx := GuardHiddenHome(context.Background(), Guard{})
	for _, p := range []string{"/proc/self/environ", "/proc/1/environ", "/proc/self/cmdline", "/proc/self/mem", "/proc/self/task/1/environ"} {
		if _, err := Path(ctx, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("read %s: %v", p, err)
		}
	}
	if _, err := Path(context.Background(), "/proc/self/environ"); err != nil {
		t.Errorf("an approved run: %v", err)
	}
}

// On a file system that compares names without case (Windows, macOS), a
// path that differs from the home or the project in case is still in them.
func TestNamesCompareWithoutCaseWhereTheFileSystemDoes(t *testing.T) {
	old := caseFolds
	t.Cleanup(func() { caseFolds = old })
	caseFolds = true
	if !in("/Users/Ada", "/users/ada/.SSH/id") || !in("/x/Proj", "/x/proj/f") {
		t.Error("in compares with case")
	}
	caseFolds = false
	if in("/Users/Ada", "/users/ada/.ssh/id") {
		t.Error("in folds case where the file system doesn't")
	}
}

// In the project folder too, a file a tool runs the next time the user works
// there stays closed to writes; the rest of those folders, and reading, stay
// open.
func TestNoFileThatRunsOnNextUse(t *testing.T) {
	_, home := guardHome(t)
	project := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := GuardHiddenHome(context.Background(), Guard{Project: project})
	for _, rel := range []string{
		".claude/settings.json", ".claude/settings.local.json", ".mcp.json",
		".husky/pre-commit", ".husky/_/husky.sh", ".githooks/pre-push", ".envrc", "sub/.envrc",
		".claude/hooks/pre-tool.sh", ".claude/helpers/router.cjs", "CLAUDE.md", "docs/CLAUDE.md",
		".claude/commands/x.md", ".claude/agents/a.md", ".claude/skills/s/SKILL.md", ".claude/hooks",
		"CLAUDE.local.md", "AGENTS.md", "GEMINI.md", ".cursorrules", ".cursor/rules/x.mdc",
		".github/copilot-instructions.md",
		".cursor/mcp.json", ".github/workflows/ci.yml", ".windsurfrules", ".aider.conf.yml",
		".codex/config.toml", ".gemini/settings.json", ".continue/config.json", ".vscode/tasks.json",
		".zed/settings.json", ".zed/tasks.json",
		".cursor/hooks.json", ".cursor/environment.json", ".cursor/settings.json", ".windsurf/rules/r.md",
		".clinerules", ".clinerules/r.md", ".roo/mcp.json", ".roorules", ".kiro/steering/s.md", ".amazonq/rules/r.md",
		".opencode/agent/a.md", "opencode.json", ".github/agents/a.md", ".github/instructions/i.md",
		".github/prompts/p.md", ".gitlab-ci.yml", ".circleci/config.yml", ".pre-commit-config.yaml",
		"lefthook.yml", ".mise.toml", ".devcontainer/devcontainer.json", ".vscode/launch.json",
		".github/actions/setup/action.yml", ".idea/workspace.xml", ".trae/rules/r.md", ".junie/guidelines.md",
		".goosehints", ".goose/config.yaml", ".augment/rules/r.md", ".qwen/settings.json", "QWEN.md", "crush.json",
		".agents/skills/s/SKILL.md", ".agent/rules.md", ".copilot/mcp.json", "CODEX.md", "AGENT.md", "agents.md",
		"Jenkinsfile", "azure-pipelines.yml", ".travis.yml", "bitbucket-pipelines.yml", ".buildkite/pipeline.yml",
		".gitpod.yml", ".devcontainer.json",
		".crush.json", ".kilocode/rules/r.md", ".kilocodemodes", ".continuerules", ".rules", ".replit",
		".drone.yml", "cloudbuild.yaml", "appveyor.yml", ".zed/keymap.json", ".appveyor.yml", "appveyor.yaml",
		"cloudbuild.yml", "cloudbuild.json", ".drone.yaml", ".woodpecker.yml", ".woodpecker.yaml",
		".woodpecker/build.yml", ".semaphore/semaphore.yml", "bitrise.yml", "codemagic.yaml",
		".gitlab/ci/build.yml", ".claude.json", "azure-pipelines.yaml", ".gitlab-ci.yaml", ".travis.yaml",
		"buildspec.yml", "buildspec.yaml", "buildkite.yml", ".buildkite.yml", ".cirrus.yml", "wercker.yml",
		"codefresh.yml", ".gocd.yml", "concourse.yml", "netlify.toml", "vercel.json",
		"CLOUDBUILD.YML", "Claude.md", ".CLAUDE/settings.json", ".GitHub/Workflows/ci.yml", "sub/.Envrc",
	} {
		p := filepath.Join(project, filepath.FromSlash(rel))
		if _, err := WritePath(ctx, p); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("write %s: %v", rel, err)
		}
		if _, err := Path(ctx, p); err != nil {
			t.Errorf("read %s: %v", rel, err)
		}
	}
	for _, rel := range []string{".env", "husky.md", "notes/.claude-notes", "envrc.txt", "CLAUDE.md.bak", "README.md", ".github/ISSUE_TEMPLATE/bug.md", ".vscode/settings.json", ".zed/notes.md", "opencode.json.bak", "docs/lefthook.md",
		".gitlab/issue_templates/bug.md", ".gitlab/CODEOWNERS"} {
		if _, err := WritePath(ctx, filepath.Join(project, filepath.FromSlash(rel))); err != nil {
			t.Errorf("write %s: %v", rel, err)
		}
	}
	if _, err := WritePath(context.Background(), filepath.Join(project, ".envrc")); err != nil {
		t.Errorf("an approved run: %v", err)
	}
	// Where the file system folds case, so does the rule.
	old := caseFolds
	t.Cleanup(func() { caseFolds = old })
	caseFolds = true
	if _, err := WritePath(ctx, filepath.Join(project, ".CLAUDE", "Settings.json")); !errors.Is(err, ErrUnapprovedPath) {
		t.Errorf("write .CLAUDE/Settings.json on a case-folding system: %v", err)
	}
}

// Names compare with case on Linux and the other systems whose file systems
// do, and without on Windows and macOS. Nothing here resets caseFolds.
func TestCaseFoldsFollowsTheSystem(t *testing.T) {
	want := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if caseFolds != want {
		t.Fatalf("caseFolds = %v on %s, want %v", caseFolds, runtime.GOOS, want)
	}
}

// A refusal in a profile folder that doesn't count as a project folder says
// why it doesn't.
func TestARefusalSaysWhyTheProfileFolderIsNoProject(t *testing.T) {
	_, home := guardHome(t)
	hidden := filepath.Join(home, ".work", "proj")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatal(err)
	}
	for project, want := range map[string]string{hidden: "lies in a hidden folder", home: "is your home or holds it"} {
		ctx := GuardHiddenHome(context.Background(), Guard{Project: project})
		_, err := WritePath(ctx, filepath.Join(hidden, "out.csv"))
		if !errors.Is(err, ErrUnapprovedPath) || !strings.Contains(err.Error(), want) {
			t.Errorf("project %s: %v, want it to say it %s", project, err, want)
		}
	}
}

// A git directory under another name (--separate-git-dir, a worktree's
// gitdir) is found by its HEAD, objects and refs: its hooks and config stay
// closed; its other files, and a folder that merely has a HEAD, stay open.
func TestAGitDirectoryUnderAnotherNameKeepsItsHooksClosed(t *testing.T) {
	_, home := guardHome(t)
	gitdata := filepath.Join(home, "code", "gitdata")
	notGit := filepath.Join(home, "code", "notes")
	for _, d := range []string{filepath.Join(gitdata, "objects"), filepath.Join(gitdata, "refs"), notGit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lookalike := filepath.Join(home, "code", "lookalike") // HEAD, objects and refs, but HEAD is no git HEAD
	for _, d := range []string{filepath.Join(lookalike, "objects"), filepath.Join(lookalike, "refs")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for f, head := range map[string]string{
		filepath.Join(gitdata, "HEAD"):   "ref: refs/heads/main\n", // no config file: git needs none
		filepath.Join(notGit, "HEAD"):    "ref: refs/heads/main\n",
		filepath.Join(lookalike, "HEAD"): "the head of the document\n",
	} {
		if err := os.WriteFile(f, []byte(head), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx := GuardHiddenHome(context.Background(), Guard{})
	for _, rel := range []string{"hooks/pre-commit", "config", "config.worktree", "worktrees/w/config.worktree", "modules/m/hooks/x"} {
		if _, err := WritePath(ctx, filepath.Join(gitdata, filepath.FromSlash(rel))); !errors.Is(err, ErrUnapprovedPath) {
			t.Errorf("write gitdata/%s: %v", rel, err)
		}
	}
	for _, p := range []string{filepath.Join(gitdata, "description"), filepath.Join(notGit, "hooks", "x"), filepath.Join(notGit, "config"), filepath.Join(lookalike, "hooks", "x")} {
		if _, err := WritePath(ctx, p); err != nil {
			t.Errorf("write %s: %v", p, err)
		}
	}
}

// A .git file points git at another folder's hooks: an unapproved run
// writes none.
func TestNoGitPointerFile(t *testing.T) {
	_, home := guardHome(t)
	ctx := GuardHiddenHome(context.Background(), Guard{Project: filepath.Join(home, "code")})
	if _, err := WritePath(ctx, filepath.Join(home, "code", "proj", ".git")); !errors.Is(err, ErrUnapprovedPath) {
		t.Fatalf("write proj/.git: %v", err)
	}
}

func TestGitHead(t *testing.T) {
	for head, want := range map[string]bool{
		"ref: refs/heads/main": true, "0123456789abcdef0123456789abcdef01234567": true,
		strings.Repeat("a", 64): true, "hello": false, "ref: heads/main": false, strings.Repeat("g", 40): false,
	} {
		if gitHead(head) != want {
			t.Errorf("gitHead(%q) = %v", head, !want)
		}
	}
}
