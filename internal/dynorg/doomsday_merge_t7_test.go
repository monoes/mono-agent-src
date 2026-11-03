package dynorg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// killedMerge is a repository in the state a real `git merge` leaves when it is killed after it has written the
// tree and before the ref update, with the pin facts taken just before the merge started.
type killedMerge struct {
	repo, before, tip, fp string
}

// killMergeInRepo runs a real `git merge --no-ff` whose pre-merge-commit hook kills its own parent git: the only
// way to stop git at that exact point without a race (note 13.3 T7).
func killMergeInRepo(t *testing.T, conflict bool) killedMerge {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell hook")
	}
	repo := newRepo(t)
	gitT(t, repo, "checkout", "-q", "-b", "feat")
	file := "c.txt"
	if conflict {
		file = "a.txt"
	}
	if err := os.WriteFile(filepath.Join(repo, file), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "feat")
	tip := gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "checkout", "-q", "-")
	if conflict {
		if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("ours\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, repo, "commit", "-q", "-a", "-m", "ours")
	}
	k := killedMerge{repo: repo, before: gitT(t, repo, "rev-parse", "HEAD"), tip: tip, fp: repoFingerprint(context.Background(), repo)}
	hooks := t.TempDir()
	if err := os.WriteFile(filepath.Join(hooks, "pre-merge-commit"), []byte("#!/bin/sh\nkill -9 $PPID\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", repo, "-c", "core.hooksPath="+hooks, "-c", "user.name=T", "-c", "user.email=t@example.com", "merge", "--no-ff", "-m", "x", tip)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the merge was not stopped: %s", out)
	}
	return k
}

func repoState(t *testing.T, repo string) (head string, mergeHead, lock bool, status string) {
	t.Helper()
	head = gitT(t, repo, "rev-parse", "HEAD")
	_, mhErr := gitRun(context.Background(), repo, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	_, lockErr := os.Lstat(filepath.Join(repo, ".git", "index.lock"))
	return head, mhErr == nil, lockErr == nil, gitT(t, repo, "status", "--porcelain=v1")
}

// T7: the assertions of the X4 rows are the observed behaviour of the real git on this machine. A clean merge
// killed before its commit leaves a staged tree and NO merge state (so row X4b exists and is not removable); a
// conflicting one leaves MERGE_HEAD (row X4a). Neither leaves index.lock here, and HEAD has not moved.
func TestT7RealGitKilledMergeLeavesTheStatesTheTableNames(t *testing.T) {
	k := killMergeInRepo(t, false)
	head, mh, lock, status := repoState(t, k.repo)
	if head != k.before || mh || lock || !strings.Contains(status, "c.txt") {
		t.Errorf("clean merge killed: head==before:%v MERGE_HEAD:%v index.lock:%v status=%q (X4b expects an unexplained staged tree)", head == k.before, mh, lock, status)
	}
	k = killMergeInRepo(t, true)
	head, mh, lock, status = repoState(t, k.repo)
	if head != k.before || !mh || lock || !strings.Contains(status, "UU a.txt") {
		t.Errorf("conflicting merge killed: head==before:%v MERGE_HEAD:%v index.lock:%v status=%q (X4a expects MERGE_HEAD)", head == k.before, mh, lock, status)
	}
}
