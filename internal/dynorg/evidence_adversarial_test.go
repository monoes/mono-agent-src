package dynorg

import (
	"github.com/monoes/mono-agent/internal/tasks"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalReviewerBrokenHeadDoesNotRecordUnbornBase(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	r.c.cfg.Cwd = repo
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	path := filepath.Join(repo, ".git", "HEAD")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.c.recordBase(tbg, r.mc, &missionBind{session: r.sid, task: a})
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	detail, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range detail.Artifacts {
		if artifact.Kind == tasks.ArtifactBase {
			t.Fatalf("FAIL-OPEN: corrupt detached HEAD recorded operator base=%q instead of refusing unresolved start", artifact.Ref)
		}
	}
}

func TestFinalReviewerLazyFetchCannotRunAnAgentTransport(t *testing.T) {
	repo := newRepo(t)
	blob := strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD:a.txt"))
	marker := filepath.Join(t.TempDir(), "transport-ran")
	program := filepath.Join(t.TempDir(), "transport")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"}, {"remote.origin.url", "ext::" + program}, {"remote.origin.promisor", "true"}, {"remote.origin.partialclonefilter", "blob:none"}, {"protocol.ext.allow", "always"}} {
		gitT(t, repo, "config", kv[0], kv[1])
	}
	object := filepath.Join(repo, ".git", "objects", blob[:2], blob[2:])
	if err := os.Rename(object, filepath.Join(t.TempDir(), "held-blob")); err != nil {
		t.Fatal(err)
	}
	_, err := gitRun(tbg, repo, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "checkout"), "HEAD")
	t.Logf("worktree add result: %v", err)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("PROGRAM EXECUTION: operator worktree checkout lazily fetched through agent-configured ext transport despite protocol.allow=never")
	}
}
