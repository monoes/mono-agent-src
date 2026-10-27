package dynorg

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Hook scripts, the file system monitor and signature checking are pinned
// off on every git call the conductor makes, and an unusual value of
// extensions.worktreeConfig does not hide the per-worktree file. As in
// worktree_gitconfig_test.go, each test asserts state: no marker file, and
// nothing made.

var allHooks = []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "post-checkout", "post-merge",
	"pre-merge-commit", "reference-transaction", "post-index-change", "post-rewrite", "pre-auto-gc"}

// installHooks writes a hook of every kind that creates a file named after it
// in a fresh folder, and returns that folder. It also proves the hooks are
// live: a plain git commit that is not ours fires them.
func installHooks(t *testing.T, repo string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts need a POSIX shell")
	}
	marks := t.TempDir()
	hooks := filepath.Join(repo, ".git", "hooks")
	for _, h := range allHooks {
		body := "#!/bin/sh\ntouch '" + filepath.Join(marks, h) + "'\n"
		if err := os.WriteFile(filepath.Join(hooks, h), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("git", "-C", repo, "commit", "--quiet", "--allow-empty", "-m", "sanity")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(marks, "post-commit")); err != nil {
		t.Fatal("the test hooks do not fire, so the test would prove nothing")
	}
	entries, _ := os.ReadDir(marks)
	for _, e := range entries {
		_ = os.Remove(filepath.Join(marks, e.Name()))
	}
	return marks
}

func noHookRan(t *testing.T, marks string) {
	t.Helper()
	entries, _ := os.ReadDir(marks)
	var ran []string
	for _, e := range entries {
		ran = append(ran, e.Name())
	}
	if len(ran) > 0 {
		t.Fatalf("hooks ran: %v", ran)
	}
}

func TestGitCallsCarryTheHooksAndMonitorPins(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	got := gitT(t, repo, "config", "--get", "core.hooksPath")
	if got == "" || got == filepath.Join(repo, ".git", "hooks") || !(filepath.IsAbs(got) || got == "/dev/null") {
		t.Fatalf("hooks path = %q", got)
	}
	if runtime.GOOS != "windows" && got != "/dev/null" {
		t.Fatalf("hooks path = %q, want the null device", got)
	}
	if dir, err := emptyHooksDir(); err != nil || dir != got {
		t.Fatalf("emptyHooksDir = %q, %v", dir, err)
	}
	if v := gitT(t, repo, "config", "--get", "core.fsmonitor"); v != "false" {
		t.Fatalf("fsmonitor = %q", v)
	}
}

func TestRepositoryHooksDoNotRunForTheConductorsGit(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	marks := installHooks(t, repo)
	c, _ := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "h1")
	spawnWriter(t, c, "write one.txt merged")
	spawnWriter(t, c, "write two.txt unmerged")
	c.Wait(context.Background(), nil, 10*time.Second)
	if _, err := c.Merge(context.Background(), "w1"); err != nil { // add, checkpoint commit, merge
		t.Fatal(err)
	}
	c.Close() // removal, branch deletion
	if got := gitT(t, repo, "show", "monoagent/h1/w2:two.txt"); got != "unmerged" {
		t.Fatalf("the work was lost: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "one.txt")); string(b) != "merged\n" {
		t.Fatalf("the merge did not happen: %q", b)
	}
	noHookRan(t, marks)
}

func TestRepositoryHooksDoNotRunForAVerifierCheckout(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "the executor's file\n")
	r.isolatedWriter(a, "monoagent/t1/w1")
	r.verifierSees("one.txt")
	marks := installHooks(t, repo)
	c2 := r.isolatedConductor(repo, "t2")
	if _, err := c2.Handle(tbg, ToolMissionVerify, []byte(`{"task_id":`+strconv.FormatInt(a.ID, 10)+`}`)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	c2.Close()
	noHookRan(t, marks)
}

func TestCleanupKeepsAWorktreeWhoseConfigurationNamesAProgram(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	base := filepath.Join(realPath(t, repo), WorktreeDirName)
	wt := filepath.Join(base, "k1", "w1")
	gitT(t, repo, "worktree", "add", "--quiet", "-b", "monoagent/k1/w1", wt, "HEAD")
	// Same size, new content: only hashing it (through the clean filter) says it changed.
	if err := os.WriteFile(filepath.Join(wt, "a.txt"), []byte("BASE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ran")
	defineFilter(t, repo, marker, "clean")
	gitT(t, repo, "config", "core.fsmonitor", markerCmd(marker))
	r := removeTree(context.Background(), repo, base, wt, "monoagent/k1/w1")
	noMarker(t, marker)
	if r.Removed || !strings.Contains(r.Error, "kept, not inspected") || !strings.Contains(r.Error, "names a program") {
		t.Fatalf("removeTree = %+v", r)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "a.txt")); string(b) != "BASE\n" {
		t.Fatalf("the work was touched: %q", b)
	}
	if !branchExists(t, repo, "monoagent/k1/w1") {
		t.Fatal("the branch went")
	}
}

// A commit carrying a signature header makes `merge` run a program when
// verification is on; the program here is the operator's own (global), which
// the key check trusts, so only the pin stands between it and the merge.
func TestMergeDoesNotVerifySignatures(t *testing.T) {
	global := isolateGit(t)
	repo := newRepo(t)
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "from the writer\n")
	raw := gitT(t, repo, "cat-file", "commit", "refs/heads/monoagent/t1/w1")
	head, rest, _ := strings.Cut(raw, "\ncommitter ")
	_, tail, _ := strings.Cut(rest, "\n")
	committer, _, _ := strings.Cut(rest, "\n")
	signed := head + "\ncommitter " + committer + "\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n x\n -----END PGP SIGNATURE-----\n" + tail + "\n"
	cmd := exec.Command("git", "-C", repo, "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Stdin = strings.NewReader(signed)
	sha, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "update-ref", "refs/heads/monoagent/t1/w1", strings.TrimSpace(string(sha)))
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "gpg.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte("[gpg]\n\tprogram = "+filepath.ToSlash(script)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "config", "merge.verifySignatures", "true")
	if runtime.GOOS != "windows" { // the unpinned merge would run it
		raw := exec.Command("git", "-C", repo, "merge", "--no-ff", "--no-edit", "refs/heads/monoagent/t1/w1")
		_ = raw.Run()
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("the signature program does not run unpinned, so the test would prove nothing")
		}
		_ = os.Remove(marker)
		gitT(t, repo, "reset", "--hard", "--quiet", "HEAD")
	}
	res, err2, _ := storedMerge(t, repo)
	if err2 != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err2)
	}
	noMarker(t, marker)
}

func TestRefuseProgramConfigAllowsAnExplicitOffMonitorAndHooksPath(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	gitT(t, repo, "config", "core.fsmonitor", "false")
	gitT(t, repo, "config", "core.hooksPath", "")
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"true", "/some/hook-dir", "x"} {
		gitT(t, repo, "config", "core.hooksPath", v)
		wantRefusal(t, refuseProgramConfig(context.Background(), repo), "hookspath")
	}
	gitT(t, repo, "config", "core.hooksPath", "")
	gitT(t, repo, "config", "core.fsmonitor", "true")
	wantRefusal(t, refuseProgramConfig(context.Background(), repo), "core.fsmonitor")
}

func TestRefuseProgramConfigReadsTheWorktreeFileForAnyNonFalseFlag(t *testing.T) {
	for _, flag := range []string{"2", "-1", "0x1", "true"} {
		t.Run(flag, func(t *testing.T) {
			isolateGit(t)
			repo := newRepo(t)
			gitT(t, repo, "config", "extensions.worktreeConfig", flag)
			cfg := filepath.Join(repo, ".git", "config.worktree")
			if err := os.WriteFile(cfg, []byte("[filter \"w\"]\n\tsmudge = some-program\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			wantRefusal(t, refuseProgramConfig(context.Background(), repo), "filter.w.smudge")
		})
	}
	// An explicit false leaves the file alone: git does not read it either.
	isolateGit(t)
	repo := newRepo(t)
	gitT(t, repo, "config", "extensions.worktreeConfig", "false")
	if err := os.WriteFile(filepath.Join(repo, ".git", "config.worktree"), []byte("[filter \"w\"]\n\tsmudge = some-program\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
}

// When the hostile entry is in the chat folder's own repository, the worker
// runs there; the conductor runs no git for it afterwards (no checkpoint, no
// merge) and a merge of another branch is refused.
func TestWorkerInAHostileChatFolderGetsNoGitFromTheConductor(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	defineFilter(t, repo, marker, "clean", "smudge")
	head := gitT(t, repo, "rev-parse", "HEAD")
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "h2")
	spawnWriter(t, c, "write one.txt edited")
	c.Wait(context.Background(), nil, 10*time.Second)
	if _, err := c.Handle(context.Background(), ToolMerge, json.RawMessage(`{"agent_id":"w1"}`)); err == nil {
		t.Fatal("a merge of a worker with no branch went through")
	}
	c.Close()
	noMarker(t, marker)
	if gitT(t, repo, "rev-parse", "HEAD") != head || gitT(t, repo, "diff", "--cached", "--name-only") != "" {
		t.Fatal("the conductor committed or staged in the chat folder")
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "one.txt")); string(b) != "edited\n" {
		t.Fatalf("the worker did not run in the chat folder: %q", b)
	}
	if n := notices(em, NoticeGitConfigRefused); len(n) != 1 {
		t.Fatalf("notices = %v", n)
	}
}

// A hook placed in a decoy folder beside where a temporary hooks folder
// would have been made does not run, and the pinned path names no folder.
func TestHooksInADecoyFolderDoNotRun(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	marks := installHooks(t, repo)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TEMP", tmp)
	decoy := filepath.Join(tmp, "monoagent-nohooks-decoy")
	if err := os.MkdirAll(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\ntouch '" + filepath.Join(marks, "decoy") + "'\n"
	for _, h := range allHooks {
		if err := os.WriteFile(filepath.Join(decoy, h), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "n.txt"), []byte("n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch := gitT(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err := commitAll(context.Background(), repo, branch, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	noHookRan(t, marks)
	if entries, _ := os.ReadDir(tmp); len(entries) != 1 {
		t.Fatalf("the conductor made something under the temp folder: %v", entries)
	}
}
