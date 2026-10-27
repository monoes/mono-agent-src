package gitevidence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

var bg = context.Background()

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoWith returns a repo with: base (README only), other (touches docs/x.md), and
// target (touches p/p.go), each its own commit on the main branch.
func repoWith(t *testing.T) (dir, base, other, target string) {
	t.Helper()
	dir = t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "README", "r")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "base")
	base = run(t, dir, "rev-parse", "HEAD")
	write(t, dir, "docs/x.md", "x")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "other")
	other = run(t, dir, "rev-parse", "HEAD")
	write(t, dir, "p/p.go", "package p")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "target")
	target = run(t, dir, "rev-parse", "HEAD")
	return
}

func commitCheck(ref string, files ...string) tasks.EvidenceCheck {
	return tasks.EvidenceCheck{Kind: "commit", Ref: ref, Files: files}
}

func TestCommitMustExistAndTouchADeclaredFile(t *testing.T) {
	dir, _, other, target := repoWith(t)
	c := Checker(dir)
	if err := c(bg, commitCheck("deadbeef", "p/p.go")); err == nil {
		t.Fatal("a bogus sha was accepted")
	}
	if err := c(bg, commitCheck(other, "p/p.go")); err == nil || !strings.Contains(err.Error(), "none of the declared") {
		t.Fatalf("a commit touching no declared file: %v", err)
	}
	if err := c(bg, commitCheck(target, "p/p.go", "zzz.go")); err != nil {
		t.Fatalf("a commit touching one declared file: %v", err)
	}
	if err := c(bg, commitCheck(strings.ToUpper(target), "p/")); err != nil {
		t.Fatalf("a full sha in capitals under a declared folder: %v", err)
	}
	if err := c(bg, commitCheck(target, "p/**")); err != nil {
		t.Fatalf("a /** folder: %v", err)
	}
	if err := c(bg, commitCheck(target)); err != nil {
		t.Fatalf("no declared files means existence only: %v", err)
	}
	if err := c(bg, tasks.EvidenceCheck{}); err != nil {
		t.Fatalf("the probe in a repo: %v", err)
	}
}

func TestHostileReferencesAndPathsAreRefused(t *testing.T) {
	dir, base, _, target := repoWith(t)
	c := Checker(dir)
	for _, ref := range []string{"-x", "--upload-pack=touch /tmp/x", "refs/heads/main", "main", "HEAD", target + "; id", "$(id)", "../../etc/passwd",
		"abc", strings.Repeat("a", 65), strings.Repeat("a", 5000), target[:6], target[:7], target[:39], "--end-of-options", target + "\n-x"} {
		if err := c(bg, commitCheck(ref, "p/p.go")); err == nil {
			t.Errorf("ref %q was accepted", ref)
		}
	}
	for _, f := range []string{"../p/p.go", "/p/p.go", "p/../../p.go", "./", ".", "-x", "p\\p.go", "../**", "p/p.go\x00"} {
		if err := c(bg, commitCheck(target, f)); err == nil {
			t.Errorf("declared path %q let a commit through", f)
		}
	}
	// a hostile entry beside a real one does not spoil the real one, nor stand in for it
	if err := c(bg, commitCheck(target, "../x", "p/p.go")); err != nil {
		t.Errorf("a real declared file beside a hostile one: %v", err)
	}
	if err := c(bg, commitCheck(base, "../README", "README")); err != nil {
		t.Errorf("root commit touching README: %v", err)
	}
}

func TestReachability(t *testing.T) {
	dir, _, _, target := repoWith(t)
	c := Checker(dir)
	// a commit on a side branch that is not a mission branch is not reachable from HEAD
	run(t, dir, "checkout", "-q", "-b", "scratch")
	write(t, dir, "p/side.go", "package p")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "side")
	side := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "checkout", "-q", "main")
	if err := c(bg, commitCheck(side, "p/side.go")); err == nil || !strings.Contains(err.Error(), "reachable") {
		t.Fatalf("a commit on a foreign branch: %v", err)
	}
	// the same commit on a mission branch counts, and its diff against the merge base is read
	run(t, dir, "branch", "-m", "scratch", "monoagent/t1/w1")
	if err := c(bg, commitCheck(side, "p/side.go")); err != nil {
		t.Fatalf("a commit on a mission branch: %v", err)
	}
	// a branch that only looks like one does not
	run(t, dir, "branch", "-m", "monoagent/t1/w1", "monoagent/t1")
	if err := c(bg, commitCheck(side, "p/side.go")); err == nil {
		t.Fatal("a branch of the wrong shape counted")
	}
	_ = target
}

func TestMergeBaseDiffCoversEarlierCommitsOfTheBranch(t *testing.T) {
	dir, _, _, _ := repoWith(t)
	run(t, dir, "checkout", "-q", "-b", "monoagent/t1/w1")
	write(t, dir, "feat/a.go", "a")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "a")
	write(t, dir, "feat/b.go", "b")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "b")
	tip := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "checkout", "-q", "main")
	if err := Checker(dir)(bg, commitCheck(tip, "feat/a.go")); err != nil {
		t.Fatalf("the branch tip changes a.go against the merge base: %v", err)
	}
}

func TestNonGitFolderIsNotApplicable(t *testing.T) {
	dir := t.TempDir()
	if err := Checker(dir)(bg, tasks.EvidenceCheck{}); !errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("probe outside git: %v", err)
	}
	if err := Checker(dir)(bg, commitCheck("deadbeef", "a")); !errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("commit outside git: %v", err)
	}
}

// ---- the store, with the real checker -------------------------------------------------

var (
	human = tasks.Actor{Kind: tasks.Human}
	w1    = tasks.Actor{Kind: tasks.Agent, Name: "w1"}
	v1    = tasks.Actor{Kind: tasks.Agent, Name: "v9"}
)

const notes = "Change p.\n\n[mission-meta] {\"files\":[\"p/p.go\"],\"size\":\"s\"}"

func claimed(t *testing.T, s *tasks.Store, n string) (tasks.Session, tasks.Task) {
	t.Helper()
	se, err := s.CreateSession(bg, "default", tasks.SessionInput{Title: "ev", Goal: "g"}, human)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.AddSessionTask(bg, "default", se.ID, tasks.SessionTaskInput{Title: "work", Notes: n, Acceptance: "ok", Kind: "task"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, worker(se, tk), 0); err != nil {
		t.Fatal(err)
	}
	return se, tk
}

func art(t *testing.T, s *tasks.Store, se tasks.Session, tk tasks.Task, kind, ref string) error {
	t.Helper()
	_, err := s.AddArtifact(bg, "default", se.ID, tk.ID, tasks.ArtifactInput{Kind: kind, Ref: ref}, worker(se, tk))
	return err
}

func status(t *testing.T, s *tasks.Store, id int64) tasks.Task {
	t.Helper()
	tk, _, err := s.Get(bg, "default", id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

func TestStoreRefusesUnrealEvidence(t *testing.T) {
	dir, _, other, target := repoWith(t)
	s := tasks.NewStore(testdb.Open(t).DB)
	s.SetEvidenceChecker(Checker(dir))
	se, tk := claimed(t, s, notes)

	if err := art(t, s, se, tk, "commit", "deadbeef"); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("a bogus commit was attached: %v", err)
	}
	if err := art(t, s, se, tk, "commit", "--upload-pack=x"); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("an option-looking ref was attached: %v", err)
	}
	// test output alone no longer carries a code task in a git work tree
	if err := art(t, s, se, tk, "test", "go test ./... ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); !errors.Is(err, tasks.ErrNoEvidence) {
		t.Fatalf("finish on test output alone: %v", err)
	}
	if err := art(t, s, se, tk, "commit", other); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); !errors.Is(err, tasks.ErrNoEvidence) {
		t.Fatalf("finish with a commit touching no declared file: %v", err)
	}
	if got := status(t, s, tk.ID); got.Status != tasks.StatusInProgress {
		t.Fatalf("a refused finish moved the task to %s", got.Status)
	}
	if err := art(t, s, se, tk, "commit", target); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); err != nil {
		t.Fatalf("finish with real evidence: %v", err)
	}
	if got := status(t, s, tk.ID); got.Status != tasks.StatusReview {
		t.Fatalf("status %s", got.Status)
	}
}

func TestVerifyRefusedWithoutCheckedEvidence(t *testing.T) {
	dir, _, other, target := repoWith(t)
	db := testdb.Open(t).DB
	plain := tasks.NewStore(db) // no checker: evidence reaches Review unchecked
	checked := tasks.NewStore(db)
	checked.SetEvidenceChecker(Checker(dir))

	se, tk := claimed(t, plain, notes)
	if err := art(t, plain, se, tk, "commit", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); err != nil {
		t.Fatal(err)
	}
	if _, err := checked.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", verifier(se, tk)); !errors.Is(err, tasks.ErrNoEvidence) {
		t.Fatalf("verify over a bogus commit: %v", err)
	}
	if status(t, plain, tk.ID).Verified {
		t.Fatal("a task with unchecked evidence was verified")
	}
	// a commit that exists but touches nothing declared does not do either
	if err := art(t, plain, se, tk, "commit", other); err != nil {
		t.Fatal(err)
	}
	if _, err := checked.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", verifier(se, tk)); !errors.Is(err, tasks.ErrNoEvidence) {
		t.Fatalf("verify over an unrelated commit: %v", err)
	}
	// the operator closes without verifying
	if _, err := checked.Move(bg, "default", tk.ID, tasks.StatusDone, tasks.Placement{}, human); err != nil {
		t.Fatalf("the operator's close: %v", err)
	}
	// and a real commit verifies
	se2, tk2 := claimed(t, plain, notes)
	if err := art(t, plain, se2, tk2, "commit", target); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.SessionFinish(bg, "default", se2.ID, tk2.ID, tasks.Outcome{Result: "done"}, worker(se2, tk2)); err != nil {
		t.Fatal(err)
	}
	if got, err := checked.VerifySessionTask(bg, "default", se2.ID, tk2.ID, true, "", verifier(se2, tk2)); err != nil || !got.Verified {
		t.Fatalf("verify over a real commit: %+v %v", got, err)
	}
}

func TestNonGitFolderAndUnfileTasksKeepTheOldRule(t *testing.T) {
	s := tasks.NewStore(testdb.Open(t).DB)
	s.SetEvidenceChecker(Checker(t.TempDir())) // not a repo
	se, tk := claimed(t, s, notes)
	if err := art(t, s, se, tk, "commit", "deadbeef"); err != nil {
		t.Fatalf("outside git an unchecked ref stays accepted: %v", err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, worker(se, tk)); err != nil {
		t.Fatalf("outside git: %v", err)
	}
	// in a repo, a task that declares no files needs nothing
	dir, _, _, _ := repoWith(t)
	g := tasks.NewStore(testdb.Open(t).DB)
	g.SetEvidenceChecker(Checker(dir))
	se2, tk2 := claimed(t, g, "Research the options.")
	if _, err := g.SessionFinish(bg, "default", se2.ID, tk2.ID, tasks.Outcome{Result: "done"}, worker(se2, tk2)); err != nil {
		t.Fatalf("a task without declared files: %v", err)
	}
}

// worker and verifier are the actors a conversation-managed session accepts: bound by their token to
// the session and the task (an unbound agent may not change that work).
func worker(se tasks.Session, tk tasks.Task) tasks.Actor {
	return tasks.Actor{Kind: tasks.Agent, Name: w1.Name, Session: se.ID, Task: tk.ID}
}

func verifier(se tasks.Session, tk tasks.Task) tasks.Actor {
	return tasks.Actor{Kind: tasks.Agent, Name: v1.Name, Session: se.ID, Task: tk.ID, Verifier: true}
}
