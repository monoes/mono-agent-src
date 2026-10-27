package dynorg

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Repository-local git configuration that names a program is refused before
// the conductor stages, checks out or merges. Every test asserts state: the
// marker file a program would create does not exist, and nothing was made.

// isolateGit keeps the operator's own configuration out of a test.
func isolateGit(t *testing.T) string {
	t.Helper()
	global := filepath.Join(t.TempDir(), "global.cfg")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return global
}

// markerCmd is a program whose only effect is to create marker.
func markerCmd(marker string) string { return "touch " + filepath.ToSlash(marker) }

func noMarker(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("a configured program ran: %s exists", marker)
	}
}

func wantRefusal(t *testing.T, err error, key string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "names a program") || !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "operator") {
		t.Fatalf("err = %v, want a refusal naming %s", err, key)
	}
}

// defineFilter writes a filter definition and the attribute that selects it.
func defineFilter(t *testing.T, repo, marker string, kinds ...string) {
	t.Helper()
	for _, k := range kinds {
		gitT(t, repo, "config", "filter.x."+k, markerCmd(marker))
	}
	attrs := []byte("*.txt filter=x merge=m\n")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), attrs, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "attributes"), attrs, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCleanRepositoryIsNotRefused(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	branch := gitT(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := commitAll(context.Background(), repo, branch, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if gitT(t, repo, "show", "HEAD:new.txt") != "x" {
		t.Fatal("the checkpoint was not committed")
	}
}

func TestCommitAllRefusesAFilterDefinition(t *testing.T) {
	for _, kind := range []string{"clean", "smudge", "process"} {
		t.Run(kind, func(t *testing.T) {
			isolateGit(t)
			repo := newRepo(t)
			marker := filepath.Join(t.TempDir(), "ran")
			defineFilter(t, repo, marker, kind)
			branch := gitT(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
			head := gitT(t, repo, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			wantRefusal(t, commitAll(context.Background(), repo, branch, "checkpoint"), "filter.x."+kind)
			noMarker(t, marker)
			if gitT(t, repo, "rev-parse", "HEAD") != head {
				t.Fatal("a commit was made")
			}
			if gitT(t, repo, "diff", "--cached", "--name-only") != "" {
				t.Fatal("files were staged")
			}
		})
	}
}

func TestSpawningAWriterRefusesAFilterDefinitionAndMakesNoWorktree(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	defineFilter(t, repo, marker, "smudge")
	gitT(t, repo, "add", ".gitattributes")
	gitT(t, repo, "commit", "--quiet", "-m", "attrs")
	c, em := newWriterConductor(t, &fileExec{}, repo, WritersIsolated, "t9")
	defer c.Close()
	spawnWriter(t, c, "investigate quietly")
	c.Wait(context.Background(), nil, 10*time.Second)
	noMarker(t, marker)
	if _, err := os.Stat(filepath.Join(repo, WorktreeDirName, "t9", "w1")); err == nil {
		t.Fatal("a worktree was made")
	}
	if b := gitT(t, repo, "branch", "--list", "monoagent/*"); b != "" {
		t.Fatalf("a branch was made: %s", b)
	}
	msgs := notices(em, NoticeWritersShared)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "names a program") || !strings.Contains(msgs[0], "filter.x.smudge") {
		t.Fatalf("notices = %v", msgs)
	}
}

func TestVerifierCheckoutRefusesAFilterDefinitionAndMakesNoWorktree(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "the executor's file\n")
	r.isolatedWriter(a, "monoagent/t1/w1")
	r.verifierSees("one.txt")
	marker := filepath.Join(t.TempDir(), "ran")
	defineFilter(t, repo, marker, "smudge")

	c2 := r.isolatedConductor(repo, "t2")
	if _, err := c2.Handle(tbg, ToolMissionVerify, []byte(`{"task_id":`+strconv.FormatInt(a.ID, 10)+`}`)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the verdict", func() bool { return r.task(a.ID).Verified })
	noMarker(t, marker)
	if w := gitT(t, repo, "worktree", "list", "--porcelain"); strings.Count(w, "worktree ") != 1 {
		t.Fatalf("worktrees:\n%s", w)
	}
}

// storedMerge merges a branch only the store knows, from a new turn.
func storedMerge(t *testing.T, repo string) (MergeResult, error, func() string) {
	t.Helper()
	r := newRig(t, rigOpts{})
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	w := r.isolatedWriter(a, "monoagent/t1/w1")
	c2 := r.isolatedConductor(repo, "t2")
	res, err := r.mergeTool(c2, w.id)
	return res, err, func() string { return gitT(t, repo, "rev-parse", "HEAD") }
}

func TestMergeRefusesAFilterDefinition(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "from the writer\n")
	marker := filepath.Join(t.TempDir(), "ran")
	defineFilter(t, repo, marker, "smudge", "clean")
	head := gitT(t, repo, "rev-parse", "HEAD")
	res, err, headNow := storedMerge(t, repo)
	wantRefusal(t, err, "filter.x.")
	noMarker(t, marker)
	if res.Merged || headNow() != head {
		t.Fatalf("merged: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(repo, "one.txt")); err == nil {
		t.Fatal("the branch's file reached the tree")
	}
}

func TestMergeRefusesAMergeDriver(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	branchWith(t, repo, "monoagent/t1/w1", "a.txt", "writer\n")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("a.txt merge=m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("lead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "--quiet", "-m", "lead")
	marker := filepath.Join(t.TempDir(), "ran")
	gitT(t, repo, "config", "merge.m.driver", markerCmd(marker)+" %A")
	head := gitT(t, repo, "rev-parse", "HEAD")
	_, err, headNow := storedMerge(t, repo)
	wantRefusal(t, err, "merge.m.driver")
	noMarker(t, marker)
	if headNow() != head {
		t.Fatal("HEAD moved")
	}
	if _, e := gitRun(context.Background(), repo, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); e == nil {
		t.Fatal("a merge was started")
	}
}

func TestRefuseProgramConfigKeys(t *testing.T) {
	isolateGit(t)
	cases := []struct{ key, want string }{
		{"filter.lfs.clean", "filter.lfs.clean"},
		{"FILTER.Big.Name.SMUDGE", "smudge"}, // subsection with a dot, mixed case
		{"filter.a.b.c.process", "process"},
		{"merge.m.driver", "merge.m.driver"},
		{"Merge.m.DRIVER", "driver"},
		{"diff.d.textconv", "diff.d.textconv"},
		{"diff.d.command", "diff.d.command"},
		{"diff.external", "diff.external"},
		{"core.fsmonitor", "core.fsmonitor"},
		{"core.hooksPath", "hookspath"},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			repo := newRepo(t)
			gitT(t, repo, "config", c.key, "some-program")
			err := refuseProgramConfig(context.Background(), repo)
			wantRefusal(t, err, "")
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(c.want)) {
				t.Fatalf("err = %v, want %s", err, c.want)
			}
			if strings.Contains(err.Error(), "some-program") {
				t.Fatalf("the value is in the message: %v", err)
			}
		})
	}
	// Names that do not run a program are not refused.
	repo := newRepo(t)
	for _, kv := range [][2]string{{"filter.x.required", "true"}, {"merge.m.name", "n"}, {"merge.m.recursive", "binary"}, {"diff.d.binary", "true"}, {"core.autocrlf", "input"}, {"user.name", "T"}} {
		gitT(t, repo, "config", kv[0], kv[1])
	}
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
}

func TestRefuseProgramConfigFollowsIncludes(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	dir := t.TempDir()
	inc := filepath.Join(dir, "more.cfg")
	if err := os.WriteFile(inc, []byte("[filter \"y\"]\n\tclean = some-program\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "config", "include.path", inc)
	wantRefusal(t, refuseProgramConfig(context.Background(), repo), "filter.y.clean")

	// through includeIf, from the included file, and relative to the including one
	repo2 := newRepo(t)
	outer := filepath.Join(dir, "outer.cfg")
	if err := os.WriteFile(outer, []byte("[include]\n\tpath = more.cfg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo2, "config", "includeIf.gitdir:"+filepath.ToSlash(repo2)+"/.path", outer)
	wantRefusal(t, refuseProgramConfig(context.Background(), repo2), "filter.y.clean")
}

func TestRefuseProgramConfigIgnoresTheOperatorsOwnConfiguration(t *testing.T) {
	global := isolateGit(t)
	if err := os.WriteFile(global, []byte("[filter \"lfs\"]\n\tclean = git-lfs clean -- %f\n\tsmudge = git-lfs smudge -- %f\n\tprocess = git-lfs filter-process\n[merge \"m\"]\n\tdriver = some-program\n[core]\n\thooksPath = /somewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repo := newRepo(t)
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
}

func TestRefuseProgramConfigReadsThePerWorktreeConfiguration(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	gitT(t, repo, "config", "extensions.worktreeConfig", "true")
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "config", "--worktree", "filter.w.smudge", "some-program")
	wantRefusal(t, refuseProgramConfig(context.Background(), repo), "filter.w.smudge")
	// and from a linked worktree, whose own file is the one that holds it
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, repo, "config", "--worktree", "--unset", "filter.w.smudge")
	gitT(t, repo, "worktree", "add", "--quiet", "-b", "side", wt)
	gitT(t, wt, "config", "--worktree", "filter.v.clean", "some-program")
	wantRefusal(t, refuseProgramConfig(context.Background(), wt), "filter.v.clean")
	if err := refuseProgramConfig(context.Background(), repo); err != nil {
		t.Fatalf("the main worktree: %v", err)
	}
}

func TestRefuseProgramConfigFailsClosedOnAFolderThatIsNotARepository(t *testing.T) {
	needGit(t)
	isolateGit(t)
	if err := refuseProgramConfig(context.Background(), t.TempDir()); err == nil {
		t.Fatal("a folder that is no repository was accepted")
	}
}

// A signing program in the repository's configuration is not reached: the merge runs with signing off.
func TestMergeDoesNotRunASigningProgram(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	branchWith(t, repo, "monoagent/t1/w1", "one.txt", "from the writer\n")
	marker := filepath.Join(t.TempDir(), "ran")
	gitT(t, repo, "config", "commit.gpgsign", "true")
	gitT(t, repo, "config", "gpg.program", markerCmd(marker))
	res, err, _ := storedMerge(t, repo)
	if err != nil || !res.Merged {
		t.Fatalf("merge = %+v, %v", res, err)
	}
	noMarker(t, marker)
}
