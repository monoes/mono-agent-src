package gitevidence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/testdb"
)

func scoped(dir, base string, ref string, files ...string) tasks.EvidenceCheck {
	return tasks.EvidenceCheck{Scoped: true, Dir: dir, Base: base, Kind: "commit", Ref: ref, Files: files}
}

func TestShortReferenceIsRefusedAndNoBranchRedirectsAFullOne(t *testing.T) {
	dir, _, other, target := repoWith(t)
	c := Checker(dir)
	err := c(bg, commitCheck(target[:7], "p/p.go"))
	if err == nil || !strings.Contains(err.Error(), "full object id") || !strings.Contains(err.Error(), "git rev-parse HEAD") {
		t.Fatalf("a short ref: %v", err)
	}
	// a branch named like another commit's full id points at target: the id still names its own commit
	run(t, dir, "branch", other, target)
	err = c(bg, commitCheck(other, "p/p.go"))
	if err == nil || !strings.Contains(err.Error(), "none of the declared") {
		t.Fatalf("a lookalike branch redirected the commit: %v", err)
	}
	if err := c(bg, commitCheck(target, "p/p.go")); err != nil {
		t.Fatalf("the full id: %v", err)
	}
	// a tag object is not a commit id
	run(t, dir, "tag", "-a", "-m", "m", "v1", target)
	tag := run(t, dir, "rev-parse", "v1")
	if err := c(bg, commitCheck(tag, "p/p.go")); err == nil {
		t.Fatal("a tag object id was taken for its commit")
	}
}

func TestMergeCommitIsNotEvidence(t *testing.T) {
	dir, _, _, _ := repoWith(t)
	run(t, dir, "checkout", "-q", "-b", "side")
	write(t, dir, "feat/a.go", "a")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "side work")
	run(t, dir, "checkout", "-q", "main")
	write(t, dir, "m.txt", "m")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "main work")
	run(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "side")
	merge := run(t, dir, "rev-parse", "HEAD")
	err := Checker(dir)(bg, commitCheck(merge, "feat/a.go"))
	if err == nil || !strings.Contains(err.Error(), "merge commit") {
		t.Fatalf("a merge commit that brings in a declared file: %v", err)
	}
	if err := Checker(dir)(bg, commitCheck(merge)); err == nil {
		t.Fatal("a merge commit was attached")
	}
}

func TestCommitFromBeforeTheTaskStartedIsRefused(t *testing.T) {
	dir, base, other, target := repoWith(t)
	c := Checker(dir)
	// the task started at target: target and everything before it is old
	for _, ref := range []string{target, other, base} {
		err := c(bg, scoped(dir, target, ref))
		if err == nil || !strings.Contains(err.Error(), "already part of the work tree") {
			t.Fatalf("an old commit %s: %v", ref[:7], err)
		}
	}
	// started at other: target is new work
	if err := c(bg, scoped(dir, other, target, "p/p.go")); err != nil {
		t.Fatalf("a new commit: %v", err)
	}
	// a commit made after the start is new, one that touches a declared file passes
	write(t, dir, "q/q.go", "package q")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "new")
	fresh := run(t, dir, "rev-parse", "HEAD")
	if err := c(bg, scoped(dir, target, fresh, "q/q.go")); err != nil {
		t.Fatalf("a commit after the start: %v", err)
	}
	// a start point git cannot read is not held against the commit
	if err := c(bg, scoped(dir, strings.Repeat("a", 40), fresh, "q/q.go")); err != nil {
		t.Fatalf("an unreadable start point: %v", err)
	}
	// a branch's earlier commits that the start already held do not count for the tip
	run(t, dir, "checkout", "-q", "-b", "monoagent/t1/w1", base)
	write(t, dir, "feat/a.go", "a")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "a")
	a := run(t, dir, "rev-parse", "HEAD")
	write(t, dir, "feat/b.go", "b")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "b")
	tip := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "checkout", "-q", "main")
	if err := c(bg, scoped(dir, base, tip, "feat/a.go")); err != nil {
		t.Fatalf("the branch since the start: %v", err)
	}
	if err := c(bg, scoped(dir, a, tip, "feat/a.go")); err == nil {
		t.Fatal("a file changed before the start counted for the branch tip")
	}
}

func TestMissionChecksUseTheSessionFolderNotTheProcessDirectory(t *testing.T) {
	dir, start, _, target := repoWith(t)
	other, _, _, _ := repoWith(t)
	write(t, other, "p/p.go", "package p // the process directory's own work")
	run(t, other, "add", "-A")
	run(t, other, "commit", "-q", "-m", "foreign")
	foreign := run(t, other, "rev-parse", "HEAD")
	c := Checker("")

	// the process sits in a folder with no repository: a mission is still checked
	t.Chdir(t.TempDir())
	if err := c(bg, scoped(dir, start, strings.Repeat("a", 40), "p/p.go")); err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("a non-git process directory switched the check off: %v", err)
	}
	if err := c(bg, scoped(dir, start, target, "p/p.go")); err != nil {
		t.Fatalf("the session folder's commit: %v", err)
	}
	// the process sits in another repository: its commits are not the mission's
	if err := os.Chdir(other); err != nil {
		t.Fatal(err)
	}
	if err := c(bg, scoped(dir, start, foreign, "p/p.go")); err == nil || !strings.Contains(err.Error(), "no such commit") {
		t.Fatalf("a commit of the process directory's repository was accepted: %v", err)
	}
	// and a session folder that is no repository (or unknown) keeps the old rule
	if err := c(bg, scoped(t.TempDir(), "", foreign, "p/p.go")); !errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("a non-git session folder: %v", err)
	}
	if err := c(bg, scoped("", "", foreign, "p/p.go")); err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("an unresolved session folder fell back to the process directory or the old rule: %v", err)
	}
	// without a conversation the process directory is still what is checked
	if err := c(bg, commitCheck(foreign, "p/p.go")); err != nil {
		t.Fatalf("an operator's session: %v", err)
	}
}

func TestGitEnvironmentIgnoresReplacements(t *testing.T) {
	t.Setenv("GIT_REPLACE_REF_BASE", "refs/other/")
	t.Setenv("GIT_NO_REPLACE_OBJECTS", "0")
	var base, no int
	for _, e := range cleanEnv() {
		switch {
		case strings.HasPrefix(e, "GIT_REPLACE_REF_BASE="):
			base++
		case e == "GIT_NO_REPLACE_OBJECTS=1":
			no++
		case strings.HasPrefix(e, "GIT_NO_REPLACE_OBJECTS="):
			t.Fatalf("a conflicting %s", e)
		}
	}
	if base != 0 || no != 1 {
		t.Fatalf("GIT_REPLACE_REF_BASE %d times, GIT_NO_REPLACE_OBJECTS=1 %d times", base, no)
	}
}

func TestUnreadableWorkTreeHint(t *testing.T) {
	err := unreadable(&gitError{err: errors.New("exit status 128"), code: 128, stderr: "fatal: detected dubious ownership in repository at '/x'"})
	if !strings.Contains(err.Error(), "safe.directory") {
		t.Fatalf("%v", err)
	}
	if err := unreadable(&gitError{err: errors.New("x"), stderr: "fatal: other"}); strings.Contains(err.Error(), "safe.directory") {
		t.Fatalf("%v", err)
	}
}

// ---- the store: a mission session resolves its folder from its conversation -------------

func TestStoreEnforcesTheConversationFolderFromAnyProcessDirectory(t *testing.T) {
	dir, _, other, target := repoWith(t)
	h := testdb.Open(t)
	if _, err := h.DB.Exec(`INSERT INTO ai_chat_conversations (id, profile_id, backend, created_at, updated_at, mode, cwd) VALUES ('conv1','default','agent','x','x','coder',?)`, dir); err != nil {
		t.Fatal(err)
	}
	s := tasks.NewStore(h.DB)
	s.SetEvidenceChecker(Checker(""))
	t.Chdir(t.TempDir()) // the agent moved to a folder without a repository

	se, err := s.CreateSession(bg, "default", tasks.SessionInput{Title: "ev", Goal: "g", ConversationID: "conv1"}, human)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.AddSessionTask(bg, "default", se.ID, tasks.SessionTaskInput{Title: "work", Notes: notes, Acceptance: "ok", Kind: "task"}, human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveSession(bg, "default", se.ID, human); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionClaim(bg, "default", se.ID, tk.ID, w1, 0); err != nil {
		t.Fatal(err)
	}
	// the operator's process records where the task started
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, tasks.ArtifactInput{Kind: "base", Ref: other}, human); err != nil {
		t.Fatal(err)
	}
	if err := art(t, s, se, tk, "commit", strings.Repeat("b", 40)); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("a bogus commit was attached from a non-git process directory: %v", err)
	}
	if err := art(t, s, se, tk, "commit", other[:7]); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("a short commit was attached: %v", err)
	}
	if err := art(t, s, se, tk, "commit", other); !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("a commit from before the task started was attached: %v", err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, w1); !errors.Is(err, tasks.ErrNoEvidence) {
		t.Fatalf("finish with no new commit: %v", err)
	}
	if err := art(t, s, se, tk, "commit", strings.ToUpper(target)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "done"}, w1); err != nil {
		t.Fatalf("finish over new work: %v", err)
	}
	var ref string
	if err := h.DB.QueryRow(`SELECT ref FROM task_artifacts WHERE task_id = ? AND kind = 'commit' ORDER BY id DESC LIMIT 1`, tk.ID).Scan(&ref); err != nil || ref != target {
		t.Fatalf("the stored reference %q (%v), want the normalised %q", ref, err, target)
	}
}

// A mission is never checked by the old rule because git could not answer: git missing from the
// path, an unreadable folder, or a start point git fails to resolve all refuse.
func TestScopedCheckFailsClosedWhenGitCannotAnswer(t *testing.T) {
	dir, start, _, target := repoWith(t)
	c := Checker("")
	if err := c(bg, scoped(dir, start, target, "p/p.go")); err != nil {
		t.Fatalf("baseline: %v", err)
	}

	// an unreadable folder
	if os.Getuid() != 0 {
		locked := t.TempDir()
		run(t, locked, "init", "-q")
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(locked, 0o700)
		if err := c(bg, scoped(locked, start, target, "p/p.go")); err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
			t.Fatalf("an unreadable mission folder: %v", err)
		}
	}

	// git missing from the path
	t.Setenv("PATH", t.TempDir())
	if err := c(bg, scoped(dir, start, target, "p/p.go")); err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("a mission with no git on the path: %v", err)
	}
}

// A start point git fails to resolve (as opposed to one it reports is absent) refuses.
func TestUnresolvableStartRefusesRatherThanLapses(t *testing.T) {
	dir, _, _, target := repoWith(t)
	ctx, cancel := context.WithCancel(bg)
	cancel()
	err := newSince(ctx, dir, target, strings.Repeat("a", 40))
	if err == nil {
		t.Fatal("a start point git could not be asked about lapsed the rule")
	}
	if err := newSince(bg, dir, target, strings.Repeat("a", 40)); err != nil {
		t.Fatalf("a start point git reports absent: %v", err)
	}
}

func TestGitOutputIsCapped(t *testing.T) {
	dir, _, _, _ := repoWith(t)
	if _, err := gitIn(bg, dir, "x", "hash-object", "--stdin"); err != nil {
		t.Fatalf("small output: %v", err)
	}
	var w cappedWriter
	w.w, w.max = new(bytes.Buffer), 4
	if _, err := w.Write([]byte("12345")); err == nil || !w.over {
		t.Fatal("cappedWriter kept writing past its cap")
	}
}
