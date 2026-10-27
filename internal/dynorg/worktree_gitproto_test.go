package dynorg

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A per-transport allow setting in the repository's own configuration beats the
// generic one, so a pinned generic setting cannot keep a helper transport out.
// The transport allowlist in the environment cannot be widened by configuration.

// transportProgram is a program that only creates the returned marker file.
func transportProgram(t *testing.T) (program, marker string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the test program is a POSIX shell script")
	}
	marker = filepath.Join(t.TempDir(), "transport-ran")
	program = filepath.Join(t.TempDir(), "transport")
	if err := os.WriteFile(program, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return program, marker
}

func TestTransportAllowlistBeatsRepositoryConfiguration(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	program, marker := transportProgram(t)
	gitT(t, repo, "config", "protocol.ext.allow", "always")

	// Baseline: plain git, configuration alone, starts the helper.
	_ = exec.Command("git", "-C", repo, "ls-remote", "ext::"+program).Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git behaviour changed: the baseline no longer starts the helper, so this test proves nothing: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	_, err := gitRun(tbg, repo, "ls-remote", "ext::"+program)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("the conductor's git started a transport (err %v)", err)
	}
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the conductor's git ran a helper program named by repository configuration")
	}
}

func TestGitEnvAllowsNoTransportWhateverTheOperatorEnvironmentSays(t *testing.T) {
	t.Setenv("GIT_ALLOW_PROTOCOL", "https:ssh")
	cmd := exec.Command("git")
	cmd.Env = gitEnv()
	var got, found = "", false
	for _, kv := range cmd.Environ() {
		if v, ok := strings.CutPrefix(kv, "GIT_ALLOW_PROTOCOL="); ok {
			got, found = v, true
		}
	}
	if !found || got != "" {
		t.Fatalf("GIT_ALLOW_PROTOCOL for the conductor's git = %q (set: %v), want set and empty", got, found)
	}
}

// A partial clone whose remote is a helper transport allowed by a per-transport
// setting: a missing object makes git fetch lazily, which starts the helper.
func TestLazyFetchCannotRunAnAgentTransport(t *testing.T) {
	isolateGit(t)
	repo := newRepo(t)
	program, marker := transportProgram(t)
	blob := strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD:a.txt"))
	for _, kv := range [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", "ext::" + program},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
		{"protocol.ext.allow", "always"},
	} {
		gitT(t, repo, "config", kv[0], kv[1])
	}
	object := filepath.Join(repo, ".git", "objects", blob[:2], blob[2:])
	held := filepath.Join(t.TempDir(), "held-blob")
	if err := os.Rename(object, held); err != nil {
		t.Fatal(err)
	}

	// Baseline: plain git checking the tree out starts the helper.
	_ = exec.Command("git", "-C", repo, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", filepath.Join(t.TempDir(), "plain"), "HEAD").Run()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git behaviour changed: the baseline no longer starts the helper, so this test proves nothing: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	_, err := gitRun(tbg, repo, "worktree", "add", "--detach", filepath.Join(t.TempDir(), "checkout"), "HEAD")
	t.Logf("worktree add through the conductor's runner: %v", err)
	if _, serr := os.Stat(marker); serr == nil {
		t.Fatal("the conductor's worktree checkout lazily fetched through a transport named by repository configuration")
	}
}
