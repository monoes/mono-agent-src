package dynorg

import (
	"bytes"
	"errors"
	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/tasks/gitevidence"
	"os"
	"os/exec"
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
		if artifact.Kind == tasks.ArtifactBase && artifact.Ref == tasks.BaseUnborn {
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
	// positive control: plain git, with the same repository, runs the helper; without this the
	// assertion below could pass because git never tried to fetch
	plain := exec.Command("git", "-C", repo, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "control"), "HEAD")
	plain.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
	_ = plain.Run()
	if _, err := os.Stat(marker); err != nil {
		t.Skip("this git does not run the helper with plain settings; nothing to prove")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	_, err := gitRun(tbg, repo, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "checkout"), "HEAD")
	t.Logf("worktree add result: %v", err)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("PROGRAM EXECUTION: operator worktree checkout lazily fetched through agent-configured ext transport despite protocol.allow=never")
	}
}

// A folder git cannot read when the task starts records a refusing marker rather than nothing, so
// removing .git afterwards cannot bring back the self-asserted rule.
func TestUnreadableRepositoryAtStartRecordsARefusingBase(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	r.c.cfg.Cwd = repo
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	cfg := filepath.Join(repo, ".git", "config")
	original, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("[unterminated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.c.recordBase(tbg, r.mc, &missionBind{session: r.sid, task: a})
	if err := os.WriteFile(cfg, original, 0o600); err != nil {
		t.Fatal(err)
	}
	detail, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range detail.Artifacts {
		if artifact.Kind == tasks.ArtifactBase && artifact.Ref == tasks.BaseUnreadable {
			return
		}
	}
	t.Fatalf("an unreadable repository recorded no refusing base: %+v", detail.Artifacts)
}

func TestFinalRecheckBrokenHeadThenMissingRepositoryStaysRefused(t *testing.T) {
	repo := newRepo(t)
	r := newRig(t, rigOpts{})
	r.c.cfg.Cwd = repo
	a := r.add(spec{title: "a", files: "a.go"})
	r.approve()
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte(strings.Repeat("a", 40)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r.c.recordBase(tbg, r.mc, &missionBind{session: r.sid, task: a})
	detail, err := r.store.SessionTask(tbg, "default", r.sid, a.ID, operator)
	if err != nil {
		t.Fatal(err)
	}
	base := ""
	for _, art := range detail.Artifacts {
		if art.Kind == tasks.ArtifactBase {
			base = art.Ref
		}
	}
	if err := os.Rename(filepath.Join(repo, ".git"), filepath.Join(t.TempDir(), "held-git")); err != nil {
		t.Fatal(err)
	}
	err = gitevidence.Checker("")(tbg, tasks.EvidenceCheck{Scoped: true, Dir: repo, Base: base})
	if err == nil || errors.Is(err, tasks.ErrEvidenceNotApplicable) {
		t.Fatalf("unresolved initial HEAD must stay refused after repository metadata disappears; base=%q, checker=%v", base, err)
	}
}

func TestConductorGitOutputIsLimited(t *testing.T) {
	var b bytes.Buffer
	w := &limitWriter{w: &b, max: 4}
	if _, err := w.Write([]byte("12345")); err == nil || !w.over {
		t.Fatal("limitWriter kept writing past its limit")
	}
}
