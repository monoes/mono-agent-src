package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNoEvidence is the refusal to hand a code task to review with nothing to check it by.
var ErrNoEvidence = errors.New("a code task needs evidence")

// evidenceKinds are the artifacts that count as evidence of work: a commit, a diff, test
// output, a file, a log. A report is the worker's own words about the work, so it does not.
var evidenceKinds = map[string]bool{"commit": true, "diff": true, "test": true, "file": true, "log": true}

const metaPrefix = "[mission-meta] "

// declaresFiles says whether a task's notes name the files it touches: in the planner's
// closing [mission-meta] line, or in a "Files:" block. It is what declaredFiles lists, so a
// task that declares nothing the checker can match (a bare "none") is not a code task.
func declaresFiles(notes string) bool { return len(declaredFiles(notes)) > 0 }

// needsEvidence reads the task and refuses a result for a task of a session that is a code
// task (kind task, files declared) and holds no evidence artifact. Research tasks (no
// files), checks, epics and questions are exempt. With a checker the git gate has run before
// the transaction (pre): this reads the state again and uses that verdict only when nothing
// it was computed from has changed.
func (s *Store) needsEvidence(ctx context.Context, x dbx, profileID string, session, id int64, pre *evidenceVerdict) error {
	st, err := s.loadEvidence(ctx, x, profileID, session, id)
	if err != nil {
		return err
	}
	if !st.code {
		return nil
	}
	if s.checker != nil {
		// A checked store asks even with no evidence held: a git work tree needs a commit.
		if pre == nil || pre.key != st.key() {
			return evidenceMoved(id)
		}
		if pre.err != nil {
			return pre.err
		}
	}
	if st.held {
		return nil
	}
	if st.lastFail != 0 {
		return fmt.Errorf("%w: task #%d failed verification: the evidence from before is the work that failed, so finishing it again needs "+
			"new evidence (commit, diff, test, file or log) attached after the failure: `task artifact %d --kind commit|diff|test --ref REF`, then finish again: %w",
			ErrInvalid, id, id, ErrNoEvidence)
	}
	return fmt.Errorf("%w: task #%d declares files, so finishing it needs at least one artifact "+
		"(commit, diff, test, file or log): attach it with `task artifact %d --kind commit|diff|test --ref REF`, then finish again: %w",
		ErrInvalid, id, id, ErrNoEvidence)
}

// parseMetaFiles reads the files of a [mission-meta] line.
func parseMetaFiles(js string) ([]string, bool) {
	var m struct {
		Files []string `json:"files"`
	}
	if json.Unmarshal([]byte(js), &m) != nil {
		return nil, false
	}
	return m.Files, true
}
