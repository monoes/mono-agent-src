package dynorg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The repository judgement of recovery (note 13.3 section 5, X1-X7). It may only read the repository, run
// `git merge --abort` where the live code would, and report. It never resets, cleans, checks out, removes
// index.lock, a branch or a worktree.

// gitFn is the git runner of the judgement; recovery tests count and fail calls through it.
type gitFn func(ctx context.Context, dir string, args ...string) (string, error)

// repoTop is the repository's top-level directory of an isolated conductor: the chat folder minus its prefix,
// with links resolved, so the journal names the repository and not the folder as typed.
func repoTop(iso *isolation) string {
	if iso == nil {
		return ""
	}
	top := filepath.Clean(iso.cwd)
	for range strings.FieldsFunc(iso.prefix, func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		top = filepath.Dir(top)
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	return top
}

// repoFingerprint hashes what the operator would call the state of the repository's tree: the status with
// untracked files and the list of changed paths. It reads with optional locks off, so it never takes
// index.lock. An error gives an empty fingerprint, which matches nothing.
func repoFingerprint(ctx context.Context, dir string) string {
	return fingerprintWith(ctx, gitRun, dir)
}

func fingerprintWith(ctx context.Context, git gitFn, dir string) string {
	st, err := git(ctx, dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return ""
	}
	df, err := git(ctx, dir, "--no-optional-locks", "diff", "--name-only", "-z", "HEAD")
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(st))
	h.Write([]byte{0, 0})
	h.Write([]byte(df))
	return hex.EncodeToString(h.Sum(nil))
}

// Verdict is the outcome recovery gives an operation.
type Verdict struct {
	State  string
	Repair string
	Note   string
}

func needsOperator(format string, a ...any) Verdict {
	return Verdict{State: StateIncomplete, Repair: RepairNeedsOperator, Note: fmt.Sprintf(format, a...)}
}

func clean(note string) Verdict {
	return Verdict{State: StateIncomplete, Repair: RepairClean, Note: note}
}

// repoFacts are the read-only facts about the chat folder.
type repoFacts struct {
	head, mergeHead string
	lock            bool
	fp              string
}

func readFacts(ctx context.Context, git gitFn, dir string) (repoFacts, error) {
	var f repoFacts
	head, err := git(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return f, fmt.Errorf("HEAD cannot be read: %v", err)
	}
	f.head = strings.TrimSpace(head)
	if mh, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", "MERGE_HEAD"); err == nil {
		f.mergeHead = strings.TrimSpace(mh)
	}
	gd, err := git(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return f, fmt.Errorf("the git directory cannot be found: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(strings.TrimSpace(gd), "index.lock")); err == nil {
		f.lock = true
	}
	f.fp = fingerprintWith(ctx, git, dir)
	return f, nil
}

// judgeMerge decides what a host death left in the repository, by the step the journal says was reached.
// recorded reports whether the task store holds the merge record of the branch.
func judgeMerge(ctx context.Context, git gitFn, o *opView, recorded func() (bool, error)) Verdict {
	dir := o.rec.Repo
	if err := refuseProgramConfig(ctx, dir); err != nil {
		return needsOperator("the repository was not touched: %v", err)
	}
	f, err := readFacts(ctx, git, dir)
	if err != nil {
		return needsOperator("the repository was not touched: %v", err)
	}
	pin := o.Pin
	switch {
	case pin == nil: // X1, X2: nothing was pinned, so nothing in the merge step started
		if f.mergeHead != "" || f.lock {
			return needsOperator("a merge state or index.lock is present that this operation did not pin; it was left alone")
		}
		return clean("the host died before the pin was durable; nothing of the merge was started")
	case o.Step < 3: // X3: pinned, the merge step not started
		if f.head == pin.Before && f.mergeHead == "" && !f.lock && f.fp != "" && f.fp == pin.FP {
			return clean("the host died after the pin and before the merge step; the repository is as pinned")
		}
		return needsOperator("the repository differs from the pin and the merge step never started; it was left alone")
	}
	return judgeAfterMergeStep(ctx, git, o, f, recorded)
}

func judgeAfterMergeStep(ctx context.Context, git gitFn, o *opView, f repoFacts, recorded func() (bool, error)) Verdict {
	dir, pin := o.rec.Repo, o.Pin
	switch {
	case f.mergeHead != "": // X4a
		if f.mergeHead != pin.Tip || f.head != pin.Before {
			return needsOperator("a merge state is present that is not the pinned merge; it was left alone")
		}
		if _, err := git(ctx, dir, "merge", "--abort"); err != nil {
			return needsOperator("`git merge --abort` failed (%v); run it in %s", err, dir)
		}
		after, aerr := readFacts(ctx, git, dir)
		if aerr != nil || after.head != pin.Before || after.mergeHead != "" || after.fp == "" || after.fp != pin.FP {
			return needsOperator("the abort could not be verified against the pin; check `git status` in %s", dir)
		}
		return Verdict{State: StateIncomplete, Repair: RepairRolledBack, Note: "a merge left half done was aborted and the repository matches the pin"}
	case f.head != pin.Before: // X5, X6, X7
		if _, err := git(ctx, dir, "merge-base", "--is-ancestor", pin.Tip, f.head); err != nil {
			return needsOperator("HEAD is neither the pinned head nor contains the pinned tip; it was left alone")
		}
		ok, err := recorded()
		if err != nil {
			return Verdict{State: StateIncomplete, Repair: RepairAppliedUnrecorded, Note: "the tip is in HEAD; the store could not be asked whether it records it"}
		}
		if ok {
			return Verdict{State: StateFinished, Note: "the tip is in HEAD and the store records it"}
		}
		return Verdict{State: StateIncomplete, Repair: RepairAppliedUnrecorded, Note: "the tip is in HEAD and the store does not say so; nothing was written to it"}
	case f.lock || f.fp == "" || f.fp != pin.FP: // X4b, X4c
		return needsOperator("the tree or index differs from the pin, or index.lock is present, and no merge state explains it; it was left alone")
	}
	return clean("the merge step started and changed nothing")
}
