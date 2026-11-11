package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"

	"github.com/monoes/mono-agent/internal/tasks"
)

// What an agent may read of a goal's checks (mission_goal_status, `goal show`) and what the host reads to decide whether the
// lead may propose the close: ids the host wrote, closed enums and counts. Nothing the lead, a program, the checker or the person
// wrote reaches it: no spec, no excerpt, no note, no digest, no reason, no cost, no path. A value outside its closed list is
// shown as "unknown", never repeated.

// GoalChecks is the checks of a locked goal, as an agent reads them.
type GoalChecks struct {
	Checks    int           `json:"checks"`    // how many checks the host has run for the goal
	Satisfied bool          `json:"satisfied"` // the latest check is accepted: the person answered yes to every rubric criterion
	Latest    *CheckSummary `json:"latest,omitempty"`
}

// CheckSummary is one check in ids, enums and counts.
type CheckSummary struct {
	ID           int64              `json:"id"`
	Round        int64              `json:"round"`
	State        string             `json:"state"`
	Outcome      string             `json:"outcome,omitempty"`
	Machine      []CriterionSummary `json:"machine"`
	MachinePass  int                `json:"machine_pass"`
	MachineTotal int                `json:"machine_total"`
	Rubric       []CriterionSummary `json:"rubric"`
	RubricYes    int                `json:"rubric_yes"`
	RubricNo     int                `json:"rubric_no"`
	RubricTotal  int                `json:"rubric_total"`
}

// CriterionSummary is a criterion id (c1..c16) and its result (machine) or answer (rubric: yes, no, open).
type CriterionSummary struct {
	ID     string `json:"id"`
	Result string `json:"result"`
}

var (
	criterionIDRE = regexp.MustCompile(`^c[0-9]{1,2}$`)
	knownStates   = map[string]bool{CheckRunning: true, CheckDone: true, CheckAbandoned: true}
	knownOutcomes = map[string]bool{OutcomePassedMachine: true, OutcomeAccepted: true, OutcomeRejected: true,
		"failed": true, "error": true, "blocked": true, "stale": true, "cannot-check": true, "abandoned": true}
	knownResults = map[string]bool{"pass": true, "fail": true, "error": true, "timeout": true, "oversize": true, "tainted": true, "person": true, "not-run": true}
)

func enum(v string, known map[string]bool) string {
	if known[v] {
		return v
	}
	return "unknown"
}

// SummarizeCheck is the agent-readable form of a check: only the closed fields of it are read.
func SummarizeCheck(c Check) CheckSummary {
	s := CheckSummary{ID: c.ID, Round: c.Round, State: enum(c.State, knownStates), Machine: []CriterionSummary{}, Rubric: []CriterionSummary{}}
	if c.Outcome != "" {
		s.Outcome = enum(c.Outcome, knownOutcomes)
	}
	for _, r := range c.Results {
		if !criterionIDRE.MatchString(r.Criterion) {
			continue
		}
		res := enum(r.Result, knownResults)
		s.Machine = append(s.Machine, CriterionSummary{ID: r.Criterion, Result: res})
		s.MachineTotal++
		if res == "pass" {
			s.MachinePass++
		}
	}
	for _, l := range c.Rubric {
		if !criterionIDRE.MatchString(l.Criterion) {
			continue
		}
		ans := "open"
		switch l.Answer {
		case "yes":
			ans = "yes"
			s.RubricYes++
		case "no":
			ans = "no"
			s.RubricNo++
		}
		s.Rubric = append(s.Rubric, CriterionSummary{ID: l.Criterion, Result: ans})
		s.RubricTotal++
	}
	return s
}

// GoalChecks reads the mission's checks as an agent may see them. A mission with no goal or no check yields the zero value.
func (s *Store) GoalChecks(ctx context.Context, profileID string, session int64) (GoalChecks, error) {
	var out GoalChecks
	g, err := s.Get(ctx, profileID, session)
	if err != nil {
		if errors.Is(err, tasks.ErrNotFound) {
			return out, nil
		}
		return out, err
	}
	err = s.read(ctx, func(x tasks.Queryer) error {
		return x.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_goal_checks WHERE session_id = ?`, session).Scan(&out.Checks)
	})
	if err != nil {
		return out, fmt.Errorf("missionpursuit: counting the checks: %w", err)
	}
	if out.Checks == 0 {
		return out, nil
	}
	c, err := s.readCheck(ctx, profileID, session, 0, false)
	if err != nil {
		if errors.Is(err, ErrNoCheck) {
			return out, nil
		}
		return out, err
	}
	l := SummarizeCheck(c)
	out.Latest = &l
	out.Satisfied = g.State == StateLocked && c.State == CheckDone && c.Outcome == OutcomeAccepted && c.GoalHash == g.Hash
	return out, nil
}

// FinishParks counts the checks of the mission that ended without the goal being met: failed, error or rejected by the person
// (a blocked, stale or cannot-check check is the checker or the machine in the way, not the work). It is the epoch engine's
// "finish parked" count: each one earns the lead one wake-up (bounded by the engine's own limit), never a verdict.
func FinishParks(ctx context.Context, db *sql.DB, session int64) (int, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mission_goal_checks WHERE session_id = ? AND state = 'done' AND outcome IN ('failed', 'error', 'rejected')`, session).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("missionpursuit: counting the parked finishes: %w", err)
	}
	return n, nil
}

// CloseRefusal says why the lead may not propose the close of the mission, in fixed words, or "". It refuses only a mission whose
// goal is locked by a started pursuit and whose latest check is not accepted, and only when the host runs goal checks
// (checksAsked, from the host's published state) or a check exists: a mission whose goal is never checked is closed by the person's
// own judgment, as before. The person can always close the mission themselves: this stops the LEAD proposing it. A read that
// fails refuses. Only the person's answers accept a check.
func (s *Store) CloseRefusal(ctx context.Context, profileID string, session int64, checksAsked bool) string {
	g, err := s.Get(ctx, profileID, session)
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		return ""
	case err != nil:
		return "the goal could not be read, so the close is not proposed"
	case g.State != StateLocked:
		return ""
	}
	if pu, err := s.GetPursuit(ctx, profileID, session); errors.Is(err, ErrNotStarted) || (err == nil && !pu.Enabled) {
		return ""
	} else if err != nil {
		return "the pursuit could not be read, so the close is not proposed"
	}
	gc, err := s.GoalChecks(ctx, profileID, session)
	switch {
	case err != nil:
		return "the goal's checks could not be read, so the close is not proposed"
	case gc.Satisfied:
		return ""
	case !checksAsked && gc.Checks == 0:
		return ""
	case gc.Latest == nil:
		return "the goal has not been checked yet: the host checks it once the work is settled, and the criteria that are the person's wait for their answers; only the person can close the mission before that"
	}
	l := gc.Latest
	if l.Outcome == OutcomePassedMachine {
		return fmt.Sprintf("goal check #%d passed its machine criteria (%d of %d); the person has answered yes to %d of %d of the criteria that are theirs; the mission is proposed for close once they have answered every one", l.ID, l.MachinePass, l.MachineTotal, l.RubricYes, l.RubricTotal)
	}
	label := l.Outcome
	if label == "" {
		label = l.State
	}
	return fmt.Sprintf("the goal is not satisfied: the latest check is #%d (%s, %d of %d machine criteria pass); the work goes on until a check passes and the person has answered yes to every criterion that is theirs", l.ID, label, l.MachinePass, l.MachineTotal)
}
