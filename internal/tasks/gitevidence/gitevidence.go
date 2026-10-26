// Package gitevidence is the git-backed tasks.EvidenceChecker: it validates that a commit
// artifact exists in the work tree of the mission (the folder of the session's conversation),
// is reachable from the work tree's HEAD or a mission branch, is not a merge commit, is not
// part of the history the task started from, and changes a file the task declares.
//
// Every git call is an argument list run without a shell, after validating the reference
// (a full hex object id) so it cannot be read as an option or resolved as a branch name;
// nothing the caller wrote reaches git as a path or a ref name. What it does not prove: that
// the commit is the work the task asked for (only that it touches a declared path), nor that
// the declared paths are right (the planner wrote them), nor who made the commit: author and
// committer fields are not read, and an agent can commit a trivial change to a declared file.
// A work folder that is not a git work tree keeps the old rule (any artifact of an evidence kind).
package gitevidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

var (
	// commitRef is a full object id: 40 hex digits (sha1) or 64 (sha256). An abbreviation is
	// refused: git resolves a short name to a branch or tag of that name before an object.
	commitRef = regexp.MustCompile(`^([0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)
	// branchRef is a mission branch: refs/heads/monoagent/<turn>/<worker>.
	branchRef = regexp.MustCompile(`^refs/heads/monoagent/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
)

const checkTimeout = 5 * time.Second

// Checker returns the checker for evidence. A check of a mission session (Scoped) is made in
// the folder of the session's conversation, which the store reads from its database; the
// process's working directory, which an agent can change, is never consulted for it. A check
// of a session with no conversation (an operator's own) is made in dir ("" = the process's
// working directory at the time of each check).
func Checker(dir string) tasks.EvidenceChecker {
	return func(ctx context.Context, c tasks.EvidenceCheck) error {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		d := dir
		if c.Scoped {
			if c.Dir == "" {
				return tasks.ErrEvidenceNotApplicable
			}
			d = c.Dir
		}
		return check(ctx, d, c)
	}
}

func check(ctx context.Context, dir string, c tasks.EvidenceCheck) error {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("the working directory is unreadable: %w", err)
		}
		dir = wd
	}
	if !insideRepo(dir) {
		return tasks.ErrEvidenceNotApplicable
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("this is a git work tree but git is not available to check the evidence")
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return unreadable(err)
	}
	root = strings.TrimSpace(root)
	if c.Kind != tasks.ArtifactCommit {
		return nil
	}
	if !commitRef.MatchString(c.Ref) {
		return errors.New("a commit reference is the full object id (40 hex digits, 64 in a sha256 repository): use `git rev-parse HEAD`, not an abbreviation or a name")
	}
	want := strings.ToLower(c.Ref)
	full, err := resolveCommit(ctx, root, want)
	if err != nil {
		return err
	}
	if full != want {
		return errors.New("the reference is not the id of a commit")
	}
	if !reachable(ctx, root, full) {
		return errors.New("the commit is not reachable from the work tree's HEAD or a mission branch")
	}
	if n, err := parents(ctx, root, full); err != nil {
		return err
	} else if n > 1 {
		return errors.New("a merge commit is not evidence: attach the commit that made the change")
	}
	if c.Base != "" {
		if err := newSince(ctx, root, full, c.Base); err != nil {
			return err
		}
	}
	if len(c.Files) == 0 {
		return nil
	}
	return touchesDeclared(ctx, root, full, c.Base, c.Files)
}

// unreadable is the refusal of a work tree git cannot read; git's own hint is shown when the
// cause is the folder's ownership.
func unreadable(err error) error {
	var ge *gitError
	if errors.As(err, &ge) {
		low := strings.ToLower(ge.stderr)
		if strings.Contains(low, "dubious ownership") || strings.Contains(low, "safe.directory") {
			return errors.New("git could not read this work tree: it is owned by another user (git's safe.directory rule); mark the folder safe with `git config --global --add safe.directory <folder>`")
		}
	}
	return errors.New("git could not read this work tree")
}

// resolveCommit returns the commit id full names, by object id only.
func resolveCommit(ctx context.Context, root, id string) (string, error) {
	out, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", id+"^{commit}")
	if err != nil {
		return "", errors.New("no such commit in this work tree")
	}
	out = strings.ToLower(strings.TrimSpace(out))
	if !commitRef.MatchString(out) {
		return "", errors.New("git returned an unreadable object name")
	}
	return out, nil
}

// parents counts the parents of a commit.
func parents(ctx context.Context, root, full string) (int, error) {
	out, err := git(ctx, root, "rev-list", "--parents", "-n", "1", "--end-of-options", full)
	if err != nil {
		return 0, errors.New("git could not read the commit")
	}
	return len(strings.Fields(out)) - 1, nil
}

// newSince refuses a commit that is part of the history the task started from: work done
// for the task is a commit that history did not hold. A start point git cannot resolve (it
// was pruned) is not held against the commit.
func newSince(ctx context.Context, root, full, base string) error {
	if !commitRef.MatchString(base) {
		return nil
	}
	b, err := resolveCommit(ctx, root, strings.ToLower(base))
	if err != nil {
		return nil
	}
	old, err := isAncestor(ctx, root, full, b)
	if err != nil {
		return errors.New("git could not compare the commit with the start of the task")
	}
	if old {
		return errors.New("the commit was already part of the work tree when the task started: attach a commit made for this task")
	}
	return nil
}

// isAncestor reports whether a is an ancestor of (or equal to) b: exit 1 is "no", anything
// else that fails is an error.
func isAncestor(ctx context.Context, root, a, b string) (bool, error) {
	_, err := git(ctx, root, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	var ge *gitError
	if errors.As(err, &ge) && ge.code == 1 {
		return false, nil
	}
	return false, err
}

// insideRepo walks up from dir looking for a .git entry; it reads the file system only.
func insideRepo(dir string) bool {
	d, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return true
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

func reachable(ctx context.Context, root, full string) bool {
	if _, err := git(ctx, root, "merge-base", "--is-ancestor", full, "HEAD"); err == nil {
		return true
	}
	out, err := git(ctx, root, "for-each-ref", "--contains", full, "--format=%(refname)", "refs/heads/monoagent/")
	if err != nil {
		return false
	}
	for _, ref := range strings.Split(out, "\n") {
		if branchRef.MatchString(strings.TrimSpace(ref)) {
			return true
		}
	}
	return false
}

// touchesDeclared is the file rule: at least one declared file, or a path under a declared
// folder, is changed by the commit itself or by the non-merge commits between it and its
// merge base with HEAD that the task's start did not already hold. Declared entries that
// are absolute, climb out of the tree or name the whole tree are ignored; when none remain
// the commit is refused.
func touchesDeclared(ctx context.Context, root, full, base string, declared []string) error {
	files, folders := cleanDeclared(declared)
	if len(files)+len(folders) == 0 {
		return errors.New("none of the declared files is a path inside the work tree")
	}
	changed, err := changedBy(ctx, root, full, base)
	if err != nil {
		return err
	}
	for _, c := range changed {
		for _, f := range files {
			if c == f {
				return nil
			}
		}
		for _, d := range folders {
			if strings.HasPrefix(c, d) {
				return nil
			}
		}
	}
	return errors.New("the commit changes none of the declared files")
}

// maxBranchCommits bounds how many commits of a branch are read for their files.
const maxBranchCommits = "100"

func changedBy(ctx context.Context, root, full, base string) ([]string, error) {
	// the commit is not a merge here (refused before), so its own changes are against its parent
	out, err := git(ctx, root, "-c", "core.quotepath=off", "diff-tree", "-r", "-z", "--name-only", "--no-commit-id", "--root", "--end-of-options", full)
	if err != nil {
		return nil, errors.New("git could not list the files of the commit")
	}
	names := splitZ(out)
	mb, err := git(ctx, root, "merge-base", full, "HEAD")
	if err != nil {
		return names, nil
	}
	if mb = strings.TrimSpace(mb); mb == "" || mb == full {
		return names, nil
	}
	rng := []string{"-c", "core.quotepath=off", "log", "--no-merges", "-z", "--name-only", "--format=", "--max-count=" + maxBranchCommits, full, "^" + mb}
	if commitRef.MatchString(base) {
		if b, err := resolveCommit(ctx, root, strings.ToLower(base)); err == nil {
			rng = append(rng, "^"+b)
		}
	}
	if more, err := git(ctx, root, append(rng, "--")...); err == nil {
		names = append(names, splitZ(more)...)
	}
	return names, nil
}

func splitZ(s string) []string {
	var out []string
	for _, n := range strings.Split(s, "\x00") {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// cleanDeclared turns declared entries into exact paths and folder prefixes ending in "/".
func cleanDeclared(declared []string) (files, folders []string) {
	for _, raw := range declared {
		p := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "`'\""))
		if p == "" || len(p) > 1024 || strings.ContainsAny(p, "\x00\\") || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "-") {
			continue
		}
		folder := false
		for _, suf := range []string{"/**", "/*", "/"} {
			if strings.HasSuffix(p, suf) {
				p, folder = strings.TrimSuffix(p, suf), true
				break
			}
		}
		p = path.Clean(p)
		if p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "-") {
			continue
		}
		if folder {
			folders = append(folders, p+"/")
		} else {
			files = append(files, p)
		}
	}
	return files, folders
}

// gitError is a failed git call: its exit code (-1 when it did not exit) and stderr.
type gitError struct {
	err    error
	code   int
	stderr string
}

func (e *gitError) Error() string { return e.err.Error() }
func (e *gitError) Unwrap() error { return e.err }

// git runs one git command in dir. Repository-redirecting variables are dropped, hooks and
// the file system monitor are off, replacement objects are ignored and nothing is read from
// a terminal.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = cleanEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		code := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		return "", &gitError{err: err, code: code, stderr: errb.String()}
	}
	return out.String(), nil
}

func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
			"GIT_CEILING_DIRECTORIES", "GIT_NAMESPACE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS":
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1")
}
