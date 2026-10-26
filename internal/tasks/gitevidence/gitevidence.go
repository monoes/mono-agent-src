// Package gitevidence is the git-backed tasks.EvidenceChecker: it validates that a commit
// artifact exists in the work tree the process works in, is reachable from the work tree's
// HEAD or a mission branch, and changes a file the task declares.
//
// Every git call is an argument list run without a shell, after validating the reference
// (hex only) so it cannot be read as an option; nothing the caller wrote reaches git as a
// path or a ref name. What it does not prove: that the commit is the work the task asked
// for (only that it touches a declared path), nor that the declared paths are right (the
// planner wrote them).
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
	commitRef = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)
	// branchRef is a mission branch: refs/heads/monoagent/<turn>/<worker>.
	branchRef = regexp.MustCompile(`^refs/heads/monoagent/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}/[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)
)

const checkTimeout = 20 * time.Second

// Checker returns the checker for the work tree at dir ("" = the process's working
// directory at the time of each check).
func Checker(dir string) tasks.EvidenceChecker {
	return func(ctx context.Context, c tasks.EvidenceCheck) error {
		ctx, cancel := context.WithTimeout(ctx, checkTimeout)
		defer cancel()
		return check(ctx, dir, c)
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
		return errors.New("git could not read this work tree")
	}
	root = strings.TrimSpace(root)
	if c.Kind == "" {
		return nil
	}
	if c.Kind != tasks.ArtifactCommit {
		return nil
	}
	if !commitRef.MatchString(c.Ref) {
		return errors.New("a commit reference is 7 to 64 hex digits")
	}
	full, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "--end-of-options", c.Ref+"^{commit}")
	if err != nil {
		return errors.New("no such commit in this work tree")
	}
	full = strings.TrimSpace(full)
	if !commitRef.MatchString(full) {
		return errors.New("git returned an unreadable object name")
	}
	if !reachable(ctx, root, full) {
		return errors.New("the commit is not reachable from the work tree's HEAD or a mission branch")
	}
	if len(c.Files) == 0 {
		return nil
	}
	return touchesDeclared(ctx, root, full, c.Files)
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
// folder, is changed by the commit itself or by the commits between it and its merge base
// with HEAD. Declared entries that are absolute, climb out of the tree or name the whole
// tree are ignored; when none remain the commit is refused.
func touchesDeclared(ctx context.Context, root, full string, declared []string) error {
	files, folders := cleanDeclared(declared)
	if len(files)+len(folders) == 0 {
		return errors.New("none of the declared files is a path inside the work tree")
	}
	changed, err := changedBy(ctx, root, full)
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

func changedBy(ctx context.Context, root, full string) ([]string, error) {
	out, err := git(ctx, root, "-c", "core.quotepath=off", "diff-tree", "-r", "-z", "--name-only", "--no-commit-id", "--root", "-m", full)
	if err != nil {
		return nil, errors.New("git could not list the files of the commit")
	}
	names := splitZ(out)
	if mb, err := git(ctx, root, "merge-base", full, "HEAD"); err == nil {
		if mb = strings.TrimSpace(mb); mb != "" && mb != full {
			if more, err := git(ctx, root, "-c", "core.quotepath=off", "diff", "-z", "--name-only", mb, full, "--"); err == nil {
				names = append(names, splitZ(more)...)
			}
		}
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

// git runs one git command in dir. Repository-redirecting variables are dropped, hooks and
// the file system monitor are off, and nothing is read from a terminal.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	full := append([]string{"-C", dir, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = cleanEnv()
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		switch k {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_NAMESPACE":
			continue
		}
		env = append(env, e)
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
}
