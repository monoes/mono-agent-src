package gitevidence

import (
	"errors"
	"github.com/monoes/mono-agent/internal/tasks"
	"os"
	"path/filepath"
	"testing"
)

func TestFinalReviewerGraftedHistoryCannotMakeOldWorkNew(t *testing.T) {
	dir, _, old, start := repoWith(t)
	run(t, dir, "branch", "monoagent/turn/worker", old)
	if err := Checker("")(bg, scoped(dir, start, old, "docs/x.md")); err == nil {
		t.Fatal("baseline old commit accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Checker("")(bg, scoped(dir, start, old, "docs/x.md")); err == nil {
		t.Fatal("FAIL-OPEN: an old commit passed after grafting the start point into a root")
	} else {
		t.Logf("grafts refused: %v", err)
	}
}

func TestFinalReviewerShallowHistoryCannotMakeOldWorkNew(t *testing.T) {
	dir, _, old, start := repoWith(t)
	run(t, dir, "branch", "monoagent/turn/worker", old)
	if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Checker("")(bg, scoped(dir, start, old, "docs/x.md")); err == nil {
		t.Fatal("FAIL-OPEN: an old commit passed after marking the start point shallow")
	} else {
		t.Logf("shallow refused: %v", err)
	}
}

func TestFinalReviewerReplacedRepositoryWithMissingBaseIsRefused(t *testing.T) {
	dir, _, _, start := repoWith(t)
	if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(t.TempDir(), "old-dotgit")); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q", "-b", "other-repository")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "snapshot of pre-existing files")
	head := run(t, dir, "rev-parse", "HEAD")
	if err := Checker("")(bg, scoped(dir, start, head, "p/p.go")); err == nil {
		t.Fatal("FAIL-OPEN: replaced repository passed despite recorded base absent from its object database")
	} else {
		t.Logf("replacement refused: %v", err)
	}
}

func TestFinalReviewerDiffEdgeCases(t *testing.T) {
	dir, _, _, start := repoWith(t)
	run(t, dir, "update-index", "--chmod=+x", "p/p.go")
	run(t, dir, "commit", "-q", "-m", "mode only")
	mode := run(t, dir, "rev-parse", "HEAD")
	t.Logf("mode-only check: %v", Checker("")(bg, scoped(dir, start, mode, "p/p.go")))
	run(t, dir, "mv", "p/p.go", "p/renamed.go")
	run(t, dir, "commit", "-q", "-m", "rename only")
	rename := run(t, dir, "rev-parse", "HEAD")
	t.Logf("rename old path: %v; new path: %v", Checker("")(bg, scoped(dir, start, rename, "p/p.go")), Checker("")(bg, scoped(dir, start, rename, "p/renamed.go")))
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "empty")
	empty := run(t, dir, "rev-parse", "HEAD")
	if err := Checker("")(bg, scoped(dir, start, empty, "p/renamed.go")); err == nil {
		t.Error("empty HEAD accepted")
	} else {
		t.Logf("empty HEAD refused: %v", err)
	}
}

func TestFinalReviewerRevertedBranchCannotLaunderEmptyEvidence(t *testing.T) {
	dir, _, _, start := repoWith(t)
	run(t, dir, "checkout", "-q", "-b", "monoagent/turn/worker")
	write(t, dir, "p/p.go", "package p // change")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "change")
	run(t, dir, "revert", "--no-edit", "HEAD")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "empty tip")
	tip := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "checkout", "-q", "main")
	if got := run(t, dir, "diff", "--name-only", start, tip); got != "" {
		t.Fatalf("probe must have no net diff: %q", got)
	}
	if err := Checker("")(bg, scoped(dir, start, tip, "p/p.go")); err == nil {
		t.Fatal("ACCEPTED: an empty tip with no net branch change passed on union of reverted earlier diffs")
	} else {
		t.Logf("net-empty branch refused: %v", err)
	}
}

func TestFinalReviewerOldCommitCanBecomeVerifiedWithGrafts(t *testing.T) {
	dir, _, _, old := repoWith(t)
	write(t, dir, "later.txt", "newer starting point")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "start")
	start := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "branch", "monoagent/turn/worker", old)
	s, se, tk, _ := missionTask(t, dir, false)
	if _, err := s.AddArtifact(bg, "default", se.ID, tk.ID, tasks.ArtifactInput{Kind: tasks.ArtifactBase, Ref: start}, human); err != nil {
		t.Fatal(err)
	}
	if err := art(t, s, se, tk, "commit", old); err == nil {
		t.Fatal("baseline old evidence accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := art(t, s, se, tk, "commit", old); err != nil {
		return // refused at the attach: the old commit never becomes evidence
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "old work"}, w1); err != nil {
		t.Fatalf("probe finish refused: %v", err)
	}
	got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v1)
	if err != nil {
		t.Fatalf("probe verify refused: %v", err)
	}
	if got.Verified {
		t.Fatal("FAIL-OPEN end-to-end: task became verified with a commit from before its recorded operator base")
	}
}

func TestFinalReviewerMalformedIncludeCannotDisableMissionChecking(t *testing.T) {
	dir, _, _, _ := repoWith(t)
	s, se, tk, _ := missionTask(t, dir, false)
	bad := filepath.Join(t.TempDir(), "not a git repository")
	run(t, dir, "config", "include.path", bad)
	if err := os.WriteFile(bad, []byte("[unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	probe := Checker("")(bg, tasks.EvidenceCheck{Scoped: true, Dir: dir})
	t.Logf("malformed config probe: %v", probe)
	if err := art(t, s, se, tk, "log", "self-asserted.log"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionFinish(bg, "default", se.ID, tk.ID, tasks.Outcome{Result: "no checked commit"}, w1); err != nil {
		return // refused at the hand-back: the check did not fall back to the self-asserted rule
	}
	got, err := s.VerifySessionTask(bg, "default", se.ID, tk.ID, true, "", v1)
	if err != nil {
		t.Fatalf("verify refused: %v", err)
	}
	if got.Verified {
		t.Fatal("FAIL-OPEN end-to-end: malformed include path disabled git checking; verified with only a self-asserted log and no base")
	}
}

func TestFinalReviewerCheckerDoesNotRunAttributePrograms(t *testing.T) {
	dir, _, _, base := repoWith(t)
	write(t, dir, ".gitattributes", "p/p.go diff=review filter=review\n")
	write(t, dir, "p/p.go", "package p // actual new work")
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "-m", "actual work")
	head := run(t, dir, "rev-parse", "HEAD")
	marker := filepath.Join(t.TempDir(), "ran")
	program := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n: > '"+marker+"'\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"diff.external", "diff.review.command", "diff.review.textconv", "filter.review.clean"} {
		run(t, dir, "config", key, program)
	}
	if err := Checker("")(bg, scoped(dir, base, head, "p/p.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("attribute program executed by readonly checker")
	}
}

func TestUnreadableBaseRefusesWhateverTheFolderLooksLikeLater(t *testing.T) {
	for _, dir := range []string{t.TempDir(), func() string { d, _, _, _ := repoWith(t); return d }()} {
		err := Checker("")(bg, tasks.EvidenceCheck{Scoped: true, Dir: dir, Base: tasks.BaseUnreadable})
		if err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
			t.Fatalf("%s: %v", dir, err)
		}
	}
}
