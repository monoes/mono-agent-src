package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrEvidenceNotApplicable is what an EvidenceChecker returns when the work folder is not a
// git work tree: the evidence rule then stays what it was (any artifact of an evidence kind).
var ErrEvidenceNotApplicable = errors.New("tasks: evidence cannot be checked here: not a git work tree")

// EvidenceCheck is one question put to an EvidenceChecker.
//
//   - Kind "" asks whether the checker applies at all: nil means the work folder is a git
//     work tree, ErrEvidenceNotApplicable means it is not.
//   - Kind "commit" asks about one commit reference. With Files empty only the commit's
//     existence and reachability are checked (attaching it); with Files set the commit must
//     also change at least one of them (finishing and verifying the task).
//
// Files are the entries the task's notes declare, as written. The checker cleans them.
//
// Scoped, Dir and Base come from the store, never from the caller. A Scoped check is one of a
// session that belongs to a conversation (a mission): Dir is that conversation's folder, the
// only place the checker may look ("" when the store could not resolve one: not applicable),
// and the process's own working directory, which an agent can change, is not consulted. Base is
// the repository HEAD the conductor recorded when the task first started ("" when none was
// recorded): a commit already part of it is not evidence of work done since.
type EvidenceCheck struct {
	Session, Task int64
	Kind, Ref     string
	Files         []string
	Scoped        bool
	Dir, Base     string
}

// EvidenceChecker validates evidence where git is available. The store has no git access
// and stays pure: a process that has a work tree installs one with SetEvidenceChecker.
type EvidenceChecker func(ctx context.Context, c EvidenceCheck) error

// SetEvidenceChecker installs the checker. Call it before the store is used by more than
// one goroutine. With none installed (the default: personal callers, tests) artifact
// references are not validated, as before.
func (s *Store) SetEvidenceChecker(c EvidenceChecker) { s.checker = c }

// evidenceBudget bounds the whole evidence gate of one finish or verify, however many
// commits the task holds; evidenceMaxCommits is how many of them (the newest) it examines.
// The checks run before the write transaction opens, so neither holds the database lock.
var evidenceBudget = 5 * time.Second

const evidenceMaxCommits = 5

// ArtifactCommit is the artifact kind the checker validates.
const ArtifactCommit = "commit"

// checkCommitRef validates a commit reference when a checker is installed (attaching). It
// returns the reference to store: normalised (lower case) when git checked it, as typed when
// the work folder is not a git work tree.
func (s *Store) checkCommitRef(ctx context.Context, session, id int64, ref string, sc evScope) (string, error) {
	if s.checker == nil {
		return ref, nil
	}
	ctx, cancel := context.WithTimeout(ctx, evidenceBudget)
	defer cancel()
	err := s.checker(ctx, sc.check(session, id, ArtifactCommit, ref, nil))
	switch {
	case err == nil:
		return strings.ToLower(ref), nil
	case errors.Is(err, ErrEvidenceNotApplicable):
		return ref, nil
	}
	return "", invalid("the commit %q is not evidence: %v", echo(ref), err)
}

// gateEvidence runs when a task that declares files holds evidence: with a checker installed
// and the work folder a git work tree, at least one commit artifact must exist, be reachable
// and change at least one declared file or a path under a declared folder. Test, file, log
// and diff artifacts stay self-asserted and no longer carry a code task alone there. It reads
// no database: the whole gate runs inside one deadline and examines the newest commits only.
func (s *Store) gateEvidence(ctx context.Context, session int64, t Task, commits []string, sc evScope) error {
	if s.checker == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, evidenceBudget)
	defer cancel()
	files := declaredFiles(t.Notes)
	if err := s.checker(ctx, sc.check(session, t.ID, "", "", files)); err != nil {
		if errors.Is(err, ErrEvidenceNotApplicable) {
			return nil
		}
		return fmt.Errorf("%w: task #%d: the evidence could not be checked: %v: %w", ErrInvalid, t.ID, err, ErrNoEvidence)
	}
	if len(commits) > evidenceMaxCommits {
		commits = commits[len(commits)-evidenceMaxCommits:]
	}
	var first error
	for i := len(commits) - 1; i >= 0; i-- { // newest first
		ref := commits[i]
		err := s.checker(ctx, sc.check(session, t.ID, ArtifactCommit, ref, files))
		if err == nil {
			return nil
		}
		if first == nil {
			first = fmt.Errorf("commit %s: %v", echo(ref), err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	why := "it holds no commit artifact"
	if first != nil {
		why = first.Error()
	}
	return fmt.Errorf("%w: task #%d declares files in a git work tree, so its evidence must be a commit (its full object id, from `git rev-parse HEAD`) "+
		"that exists, is reachable from the work tree's HEAD or a mission branch, is not a merge commit, was made since the task started, and changes "+
		"at least one declared file (or a path under a declared folder) (%s): "+
		"attach it with `task artifact %d --kind commit --ref FULL_SHA`, then finish again: %w", ErrInvalid, t.ID, why, t.ID, ErrNoEvidence)
}

// declaredFiles lists the entries a task's notes declare, in the closing [mission-meta]
// line or a "Files:" block (one line, or the lines up to the first blank one). A trailing
// parenthesised annotation ("src/a.go (new)") is dropped, and "none" or "n/a" declares nothing.
func declaredFiles(notes string) []string {
	var out []string
	add := func(raw string) {
		if p := cleanDeclaredEntry(raw); p != "" && len(out) < 200 {
			out = append(out, p)
		}
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimRight(notes, "\n "), "\r\n", "\n"), "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, metaPrefix) {
		if m, ok := parseMetaFiles(strings.TrimPrefix(last, metaPrefix)); ok {
			for _, f := range m {
				add(f)
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	in := false
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if !in {
			if len(t) >= 6 && strings.EqualFold(t[:6], "files:") {
				in = true
				for _, part := range strings.Split(t[6:], ",") {
					add(part)
				}
			}
			continue
		}
		if t == "" {
			in = false
			continue
		}
		for _, part := range strings.Split(strings.TrimLeft(t, "-*+ \t"), ",") {
			add(part)
		}
	}
	return out
}

// cleanDeclaredEntry trims one declared entry: trailing parenthesised annotations come off,
// and the placeholders for "no files" become "".
func cleanDeclaredEntry(raw string) string {
	p := strings.TrimSpace(raw)
	for strings.HasSuffix(p, ")") {
		i := strings.LastIndex(p, "(")
		if i < 0 {
			break
		}
		p = strings.TrimSpace(p[:i])
	}
	switch strings.ToLower(strings.Trim(p, "`'\".")) {
	case "none", "n/a", "na", "nil", "-", "":
		return ""
	}
	return p
}
