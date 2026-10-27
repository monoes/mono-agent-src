package dynorg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The git and file-system helpers of isolated writers (worktree.go): the
// path guard, the checks that keep a checkpoint commit inside a worker's
// own worktree and branch, and running git.

// releaseTurnDir removes a turn's lock file and then its folder and the
// worktree folder, when they are empty.
func releaseTurnDir(base, turnDir string) {
	if lock := filepath.Join(turnDir, turnLockName); within(base, lock) {
		_ = os.Remove(lock)
	}
	removeEmptyDir(base, turnDir)
}

// within reports whether p lies strictly inside base, with symlinks in
// both resolved as far as they exist: the guard before anything under the
// worktree folder is removed. A base that is itself a symlink is refused.
func within(base, p string) bool {
	if base == "" || p == "" || filepath.Base(base) != WorktreeDirName {
		return false
	}
	b, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	if fi, err := os.Lstat(b); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir()) {
		return false
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	// Resolve the parent, not p itself: removing a symlink removes the link.
	if rp, err := filepath.EvalSymlinks(filepath.Dir(a)); err == nil {
		a = filepath.Join(rp, filepath.Base(a))
	}
	rel, err := filepath.Rel(b, a)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// isWorktreeRoot reports whether dir is the top of a git worktree (not
// just a folder inside the chat folder's repository).
func isWorktreeRoot(ctx context.Context, dir string) bool {
	top, err := gitRun(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	a, err1 := filepath.EvalSymlinks(filepath.FromSlash(strings.TrimSpace(top)))
	b, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b) // case-insensitive file systems by default
	}
	return a == b
}

// removeEmptyDir removes dir if it is empty and inside base.
func removeEmptyDir(base, dir string) {
	if within(base, dir) {
		_ = os.Remove(dir)
	}
	if fi, err := os.Lstat(base); err == nil && fi.IsDir() {
		if entries, err := os.ReadDir(base); err == nil && len(entries) == 0 {
			_ = os.Remove(base)
		}
	}
}

// checkTree refuses to work in dir unless it is the top of a worktree on
// branch: anywhere else, git would find the chat folder's repository (a
// worktree that lost its .git) or the wrong branch (a worker that checked
// out another), and a commit there would take the user's own work.
func checkTree(ctx context.Context, dir, branch string) error {
	if !isWorktreeRoot(ctx, dir) {
		return fmt.Errorf("%s is no longer a git worktree", dir)
	}
	head, err := gitRun(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return fmt.Errorf("%s is not on a branch (detached HEAD); expected %s", dir, branch)
	}
	if got := strings.TrimSpace(head); got != "refs/heads/"+branch {
		return fmt.Errorf("%s is on %s, not %s", dir, strings.TrimPrefix(got, "refs/heads/"), branch)
	}
	return nil
}

// commitAll commits every change in the worktree dir on branch, if there
// is any. It is a checkpoint: the repository's hooks and signing are skipped.
func commitAll(ctx context.Context, dir, branch, msg string) error {
	if err := checkTree(ctx, dir, branch); err != nil {
		return err
	}
	if err := refuseProgramConfig(ctx, dir); err != nil {
		return err
	}
	if _, err := gitRun(ctx, dir, "add", "--all"); err != nil {
		return err
	}
	if _, err := gitRun(ctx, dir, "diff", "--cached", "--quiet"); err == nil {
		return nil // nothing staged
	}
	args := append([]string{"-c", "commit.gpgsign=false"}, withIdentity(ctx, dir, "commit", "--quiet", "--no-verify", "-m", msg)...)
	_, err := gitRun(ctx, dir, args...)
	return err
}

// withIdentity prefixes args with a stand-in committer when neither the
// repository's config nor the environment names one, so a checkpoint
// commit or a merge never fails for the lack of one. A configured
// identity is always used as it is.
func withIdentity(ctx context.Context, dir string, args ...string) []string {
	name, _ := gitRun(ctx, dir, "config", "user.name")
	email, _ := gitRun(ctx, dir, "config", "user.email")
	env := func(k string) bool { return os.Getenv(k) != "" }
	var pre []string
	if strings.TrimSpace(name) == "" && !(env("GIT_AUTHOR_NAME") && env("GIT_COMMITTER_NAME")) {
		pre = append(pre, "-c", "user.name=mono-agent")
	}
	if strings.TrimSpace(email) == "" && !env("EMAIL") && !(env("GIT_AUTHOR_EMAIL") && env("GIT_COMMITTER_EMAIL")) {
		pre = append(pre, "-c", "user.email=mono-agent@localhost")
	}
	return append(pre, args...)
}

// gitEnvDrop are variables that would point git somewhere other than the
// folder it is run in.
var gitEnvDrop = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_PREFIX", "GIT_NAMESPACE"}

// gitEnv is the environment git runs with: this process's, without what
// would redirect it, and never prompting.
func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, d := range gitEnvDrop {
			if strings.EqualFold(k, d) {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_MERGE_AUTOEDIT=no", "LC_ALL=C")
}

// gitRun runs git in dir and returns its stdout; an error carries stderr.
func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	pins, err := gitPins()
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", append(append([]string{"-C", dir}, pins...), args...)...)
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(boundText(msg, 600))
	}
	return stdout.String(), nil
}
