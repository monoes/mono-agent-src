package dynorg

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// marked writes a script that leaves a marker file when it runs and returns both paths.
func marked(t *testing.T, name string) (script, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), name+".ran")
	script = filepath.Join(t.TempDir(), name+".sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func notRan(t *testing.T, marker, what string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("%s: a program named by the repository's configuration ran in the operator's process", what)
	}
}

// The repository's configuration (shared by every worktree of the chat folder's repository,
// so an agent's worktree can write it) must not make the operator's git run a program.
func TestGitRunIgnoresProgramsNamedByRepositoryConfiguration(t *testing.T) {
	repo := newRepo(t)
	mon, monMarker := marked(t, "fsmonitor")
	sig, sigMarker := marked(t, "gpg")
	hooks := t.TempDir()
	hookScript, hookMarker := marked(t, "hook")
	for _, h := range []string{"post-merge", "pre-merge-commit", "post-commit"} {
		if err := os.Symlink(hookScript, filepath.Join(hooks, h)); err != nil {
			t.Fatal(err)
		}
	}
	gitT(t, repo, "config", "core.fsmonitor", mon)
	gitT(t, repo, "config", "core.hooksPath", hooks)
	gitT(t, repo, "config", "log.showSignature", "true")
	gitT(t, repo, "config", "gpg.program", sig)

	// a signed commit on a side branch
	trunk := gitT(t, repo, "branch", "--show-current")
	gitT(t, repo, "checkout", "--quiet", "-b", "side")
	if err := os.WriteFile(filepath.Join(repo, "s.txt"), []byte("s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "--quiet", "--no-verify", "-m", "side")
	raw := gitT(t, repo, "cat-file", "commit", "HEAD")
	head, rest, _ := strings.Cut(raw, "\n\n")
	var lines []string
	for _, l := range strings.Split(head, "\n") {
		lines = append(lines, l)
		if strings.HasPrefix(l, "committer ") {
			lines = append(lines, "gpgsig -----BEGIN PGP SIGNATURE-----", " ", " AAAA", " -----END PGP SIGNATURE-----")
		}
	}
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n\n" + rest + "\n")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "update-ref", "refs/heads/side", strings.TrimSpace(string(out)))
	gitT(t, repo, "checkout", "--quiet", trunk)

	if _, err := gitRun(tbg, repo, "status", "--porcelain"); err != nil {
		t.Fatalf("status: %v", err)
	}
	notRan(t, monMarker, "status")
	if _, err := gitRun(tbg, repo, "log", "-n", "3", "side"); err != nil {
		t.Fatalf("log: %v", err)
	}
	notRan(t, sigMarker, "log")
	if _, err := gitRun(tbg, repo, withIdentity(tbg, repo, "merge", "--no-ff", "--no-edit", "-m", "m", "refs/heads/side")...); err != nil {
		t.Fatalf("merge: %v", err)
	}
	notRan(t, hookMarker, "merge hooks")
	notRan(t, sigMarker, "merge")
	notRan(t, monMarker, "merge")
}
