package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
type EvidenceCheck struct {
	Session, Task int64
	Kind, Ref     string
	Files         []string
}

// EvidenceChecker validates evidence where git is available. The store has no git access
// and stays pure: a process that has a work tree installs one with SetEvidenceChecker.
type EvidenceChecker func(ctx context.Context, c EvidenceCheck) error

// SetEvidenceChecker installs the checker. Call it before the store is used by more than
// one goroutine. With none installed (the default: personal callers, tests) artifact
// references are not validated, as before.
func (s *Store) SetEvidenceChecker(c EvidenceChecker) { s.checker = c }

// checkCommitRef validates a commit reference when a checker is installed (attaching).
func (s *Store) checkCommitRef(ctx context.Context, session, id int64, ref string) error {
	if s.checker == nil {
		return nil
	}
	err := s.checker(ctx, EvidenceCheck{Session: session, Task: id, Kind: ArtifactCommit, Ref: ref})
	if err == nil || errors.Is(err, ErrEvidenceNotApplicable) {
		return nil
	}
	return invalid("the commit %q is not evidence: %v", echo(ref), err)
}

// ArtifactCommit is the artifact kind the checker validates.
const ArtifactCommit = "commit"

// gateEvidence runs when a task that declares files holds evidence: with a checker installed
// and the work folder a git work tree, at least one commit artifact must exist, be reachable
// and change at least one declared file or a path under a declared folder. Test, file, log
// and diff artifacts stay self-asserted and no longer carry a code task alone there.
func (s *Store) gateEvidence(ctx context.Context, session int64, t Task, commits []string) error {
	if s.checker == nil {
		return nil
	}
	files := declaredFiles(t.Notes)
	probe := EvidenceCheck{Session: session, Task: t.ID, Files: files}
	if err := s.checker(ctx, probe); err != nil {
		if errors.Is(err, ErrEvidenceNotApplicable) {
			return nil
		}
		return fmt.Errorf("%w: task #%d: the evidence could not be checked: %v: %w", ErrInvalid, t.ID, err, ErrNoEvidence)
	}
	var first error
	for _, ref := range commits {
		err := s.checker(ctx, EvidenceCheck{Session: session, Task: t.ID, Kind: ArtifactCommit, Ref: ref, Files: files})
		if err == nil {
			return nil
		}
		if first == nil {
			first = fmt.Errorf("commit %s: %v", echo(ref), err)
		}
	}
	why := "it holds no commit artifact"
	if first != nil {
		why = first.Error()
	}
	return fmt.Errorf("%w: task #%d declares files in a git work tree, so its evidence must be a commit that exists, is reachable from "+
		"the work tree's HEAD or a mission branch, and changes at least one declared file (or a path under a declared folder) (%s): "+
		"attach it with `task artifact %d --kind commit --ref SHA`, then finish again: %w", ErrInvalid, t.ID, why, t.ID, ErrNoEvidence)
}

// declaredFiles lists the entries a task's notes declare, in the closing [mission-meta]
// line or a "Files:" block (one line, or the lines up to the first blank one).
func declaredFiles(notes string) []string {
	var out []string
	add := func(raw string) {
		if p := strings.TrimSpace(raw); p != "" && len(out) < 200 {
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
