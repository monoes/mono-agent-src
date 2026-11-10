package missionpursuit

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A RubricAnswer is the person's answer to one rubric criterion of a check.
type RubricAnswer struct {
	Check     int64  // the check read
	Criterion string // c1, c2, ...
	Yes       bool
	Note      string // up to MaxNoteBytes, the person's words
	Dossier   string // the check's dossier hash as read: compare-and-set
	// SubjectNow is the digest of the project folder the check ran against, computed by the caller NOW. It must equal the
	// one the check recorded: the project must be unchanged between the check and the person's yes.
	SubjectNow string
}

var criterionRE = regexp.MustCompile(`^c([0-9]{1,2})$`)

// AnswerRubric records the person's yes or no for one rubric criterion of the mission's latest check, which must have passed
// its machine criteria and its reviewer, and must be the check the person read (the dossier hash) over the project the check
// ran on (SubjectNow). Only the operator may. One call answers one criterion, once. A no ends the check as rejected at once; the
// last yes accepts it, in the same transaction. The answer also moves mission_pursuit.updated_at, because it is the person
// acting: the pursuit loop measures the away limits from it.
func (s *Store) AnswerRubric(ctx context.Context, profileID string, session int64, a RubricAnswer, by tasks.Actor) (Check, error) {
	if by.Kind != tasks.Human {
		return Check{}, operatorOnly("answer a mission goal check")
	}
	m := criterionRE.FindStringSubmatch(a.Criterion)
	if m == nil {
		return Check{}, invalid("name the criterion c1 to c%d, as the check shows it (got %q)", MaxCriteria, a.Criterion)
	}
	pos, _ := strconv.Atoi(m[1])
	if len(a.Note) > MaxNoteBytes {
		return Check{}, invalid("a note is up to %d bytes", MaxNoteBytes)
	}
	if !utf8.ValidString(a.Note) {
		return Check{}, invalid("the note is not valid text")
	}
	note := strings.TrimSpace(Clean(a.Note, MaxNoteBytes))
	if a.Dossier == "" {
		return Check{}, invalid("give the dossier hash `goal check` printed: the answer is for the check you read")
	}
	var out Check
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if _, err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		pu, err := readPursuit(ctx, x, session)
		if err != nil {
			return err
		}
		if !pu.Enabled {
			return ErrNotStarted
		}
		c, err := readCheckRow(ctx, x, session, a.Check)
		if err != nil {
			return err
		}
		if latest, err := readCheckRow(ctx, x, session, 0); err != nil || latest.ID != c.ID {
			return invalid("check #%d is not the latest check of this mission: read the latest one", c.ID)
		}
		if c.State != CheckDone || c.Outcome != OutcomePassedMachine || c.ReviewerVerdict != "ok" {
			return invalid("check #%d did not pass its machine criteria and its reviewer (%s %s): there is nothing for you to answer", c.ID, c.State, c.Outcome)
		}
		if dossierHash(c.ID, c.GoalHash, c.ResultsDigest, c.ReviewerVerdict, c.SubjectDigest) != a.Dossier {
			return ErrDossierStale
		}
		if a.SubjectNow == "" || a.SubjectNow != c.SubjectDigest {
			return ErrSubjectChanged
		}
		g, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if g.State != StateLocked || g.Hash != c.GoalHash {
			return invalid("the goal is not the one check #%d ran against", c.ID)
		}
		var crit *StoredCriterion
		for i := range g.Criteria {
			if g.Criteria[i].ID == a.Criterion {
				crit = &g.Criteria[i]
			}
		}
		if crit == nil || crit.Kind != KindRubric {
			return invalid("%s is not one of your rubric criteria: only those are answered by you", a.Criterion)
		}
		var have int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_goal_answers WHERE check_id = ? AND pos = ?`, c.ID, pos).Scan(&have); err != nil {
			return err
		}
		if have > 0 {
			return invalid("you answered %s for check #%d already: an answer is not changed", a.Criterion, c.ID)
		}
		answer := "no"
		if a.Yes {
			answer = "yes"
		}
		now := s.after(pu.updatedAt)
		if _, err := x.ExecContext(ctx, `INSERT INTO mission_goal_answers (check_id, pos, criterion_hash, answer, note, dossier_hash, answered_by, answered_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, pos, crit.Hash, answer, note, a.Dossier, by.Label(), now); err != nil {
			return fmt.Errorf("missionpursuit: recording the answer: %w", err)
		}
		next := ""
		if !a.Yes {
			next = OutcomeRejected
		} else {
			var open int
			if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_goal_criteria g WHERE g.session_id = ? AND g.kind = 'rubric'
			  AND NOT EXISTS (SELECT 1 FROM mission_goal_answers a WHERE a.check_id = ? AND a.pos = g.pos AND a.answer = 'yes')`, session, c.ID).Scan(&open); err != nil {
				return err
			}
			if open == 0 {
				next = OutcomeAccepted
			}
		}
		if next != "" {
			if _, err := x.ExecContext(ctx, `UPDATE mission_goal_checks SET outcome = ? WHERE id = ? AND state = 'done' AND outcome = 'passed-machine'`, next, c.ID); err != nil {
				return fmt.Errorf("missionpursuit: ending the check: %w", err)
			}
		}
		if _, err := x.ExecContext(ctx, `UPDATE mission_pursuit SET updated_at = ? WHERE session_id = ?`, now, session); err != nil {
			return fmt.Errorf("missionpursuit: recording that you acted: %w", err)
		}
		row, err := readCheckRow(ctx, x, session, c.ID)
		if err != nil {
			return err
		}
		out, err = fillCheck(ctx, x, row, true)
		return err
	})
	return out, err
}

// SetUnconfinedChecks turns the person's switch for unconfined checks on or off. Only the operator may, and only on a started
// pursuit. With it off (the default) the host refuses to run project commands that it cannot confine; with it on a check may run
// them as the person, with no confinement beyond a process group and the library's limits. The change is the person's action:
// it also moves mission_pursuit.updated_at.
func (s *Store) SetUnconfinedChecks(ctx context.Context, profileID string, session int64, on bool, by tasks.Actor) (Pursuit, error) {
	if by.Kind != tasks.Human {
		return Pursuit{}, operatorOnly("change whether goal checks may run unconfined")
	}
	var out Pursuit
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if _, err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		cur, err := readPursuit(ctx, x, session)
		if err != nil {
			return err
		}
		if !cur.Enabled {
			return ErrNotStarted
		}
		if cur.UnconfinedChecksOK == on {
			if on {
				return invalid("unconfined checks are allowed already")
			}
			return invalid("unconfined checks are not allowed")
		}
		v := 0
		if on {
			v = 1
		}
		if _, err := x.ExecContext(ctx, `UPDATE mission_pursuit SET unconfined_checks_ok = ?, unconfined_by = ?, updated_at = ? WHERE session_id = ?`, v, by.Label(), s.after(cur.updatedAt), session); err != nil {
			return fmt.Errorf("missionpursuit: changing the switch: %w", err)
		}
		out, err = readPursuit(ctx, x, session)
		return err
	})
	return out, err
}
