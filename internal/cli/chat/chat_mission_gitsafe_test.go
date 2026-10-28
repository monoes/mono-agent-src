package chat

import (
	"context"
	"os/exec"
	"testing"
)

// The work-tree probe runs git hardened: a GIT_DIR in the process environment must not make a
// plain folder look like the repository it names.
func TestCwdIsGitWorkTreeIgnoresARedirectingEnvironment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo, plain := t.TempDir(), t.TempDir()
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	t.Setenv("GIT_DIR", repo+"/.git")
	if cwdIsGitWorkTree(context.Background(), plain) {
		t.Fatal("a plain folder was taken for the repository GIT_DIR names")
	}
}
