package missionpursuit

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"github.com/monoes/mono-agent/internal/tasks"
)

// HostChecks is the one writer of a goal check and of its machine results: the host's verifier, with no person and no agent
// behind it. It is a typed capability: only internal/coderhost constructs one (NewHostChecks; a test lists every caller in
// internal/ and cmd/), no CLI verb and no MCP tool reaches it, and everything it writes says written_by = host-verifier.
// It never writes a task_session_events row and has no actor: a host check is not the person acting, so it moves none of
// the date the away limits are measured from. The zero value refuses everything.
type HostChecks struct{ s *Store }

// NewHostChecks is the host's capability over the store. Only internal/coderhost may call it.
func NewHostChecks(s *Store) HostChecks { return HostChecks{s: s} }

// BeginCheck is what a check starts with, all read by the host from its own rows.
type BeginCheck struct {
	GoalHash    string // the locked goal's hash
	Round       int64
	SubjectRoot string // the project folder the host resolved
}

// CheckCapMicro is the most one check's reviewer call may cost (the library's per-call cap), in micro-USD. A check that ends
// abandoned, and a restart's sweep, record it with cost_known = 0: the call may have been paid before the host went away.
const CheckCapMicro = int64(verifier.DefaultCallCapUSD * 1e6)

// maxCheckCostMicro bounds what Finish takes as a cost: the library charges the cap for anything it cannot believe.
const maxCheckCostMicro = 100 * CheckCapMicro

var errNoHostChecks = fmt.Errorf("%w: no host check capability", tasks.ErrInvalid)

// Begin records a check of the mission's locked goal as running, and returns its id. One check runs per mission at a time.
func (h HostChecks) Begin(ctx context.Context, profileID string, session int64, in BeginCheck) (int64, error) {
	if h.s == nil {
		return 0, errNoHostChecks
	}
	var id int64
	err := h.s.tx(ctx, func(x tasks.Queryer) error {
		if _, err := h.s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		pu, err := readPursuit(ctx, x, session)
		if err != nil {
			return err
		}
		if !pu.Enabled {
			return ErrNotStarted
		}
		g, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if g.State != StateLocked || g.Hash != in.GoalHash {
			return invalid("a check is for the locked goal as the host read it")
		}
		if len(in.SubjectRoot) > 4096 || !filepath.IsAbs(in.SubjectRoot) || filepath.Clean(in.SubjectRoot) != in.SubjectRoot {
			return invalid("the project folder is a clean absolute path (at most 4096 bytes)")
		}
		var running, last int64
		if err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(state = 'running'), 0), COALESCE(MAX(round), -1) FROM mission_goal_checks WHERE session_id = ?`, session).Scan(&running, &last); err != nil {
			return fmt.Errorf("missionpursuit: reading the checks: %w", err)
		}
		if running > 0 {
			return ErrCheckRunning
		}
		if in.Round <= last {
			return invalid("a check round is greater than every earlier round of the mission (the last is %d)", last)
		}
		res, err := x.ExecContext(ctx, `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, subject_root, started_at, written_by) VALUES (?, ?, ?, 'running', ?, ?, ?)`,
			session, g.Hash, in.Round, in.SubjectRoot, h.s.stamp(), HostVerifier)
		if err != nil {
			return fmt.Errorf("missionpursuit: starting a goal check: %w", err)
		}
		id, _ = res.LastInsertId()
		return nil
	})
	return id, err
}

// critPos reads "c7" as 7.
func critPos(id string) (int, bool) {
	if !strings.HasPrefix(id, "c") {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	return n, err == nil && n >= 1 && n <= MaxCriteria && id == "c"+strconv.Itoa(n)
}

// Add records what the host measured for one machine criterion of a running check. A rubric criterion (result "person") has
// no machine result and is not stored: the person answers it.
func (h HostChecks) Add(ctx context.Context, checkID int64, r verifier.CriterionResult) error {
	if h.s == nil {
		return errNoHostChecks
	}
	if r.Result == verifier.ResPerson || r.Kind == KindRubric {
		return nil
	}
	pos, ok := critPos(r.ID)
	if !ok {
		return invalid("a criterion id is c1 to c%d, not %q", MaxCriteria, r.ID)
	}
	runs := 0
	if r.RunsSubjectCode {
		runs = 1
	}
	_, err := h.s.db.ExecContext(ctx, `INSERT INTO mission_goal_check_results (check_id, pos, criterion_hash, kind, result, exit_code, stdout_digest, stderr_digest, stdout_excerpt, stderr_excerpt, detail, net, runs_subject_code)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		checkID, pos, r.Hash, r.Kind, r.Result, r.ExitCode, Clean(r.StdoutDigest, 200), Clean(r.StderrDigest, 200),
		Clean(r.Stdout, ExcerptShown), Clean(r.Stderr, ExcerptShown), Clean(r.Detail, 400), Clean(r.Net, 200), runs)
	if err != nil {
		return fmt.Errorf("missionpursuit: recording a check result: %w", err)
	}
	return nil
}

// Finish ends a running check with the verifier's result: done with its outcome, or abandoned when the check was cancelled
// or ran out of time. The result must be the one for this check's goal.
func (h HostChecks) Finish(ctx context.Context, checkID int64, res verifier.Result) error {
	if h.s == nil {
		return errNoHostChecks
	}
	state, outcome := CheckDone, string(res.Outcome)
	switch res.Outcome {
	case verifier.OutcomePassedMachine, verifier.OutcomeFailed, verifier.OutcomeError, verifier.OutcomeBlocked, verifier.OutcomeStale, verifier.OutcomeCannotCheck:
	case verifier.OutcomeAbandoned:
		state, outcome = CheckAbandoned, ""
	default:
		return invalid("a check cannot end with the outcome %q", res.Outcome)
	}
	switch res.ReviewerVerdict {
	case "", "ok", "block":
	default:
		return invalid("the reviewer's verdict is ok or block, not %q", res.ReviewerVerdict)
	}
	cost := res.CostMicro
	if cost < 0 || cost > maxCheckCostMicro {
		return invalid("a check costs between nothing and %d micro-USD", maxCheckCostMicro)
	}
	known := 0
	if res.CostKnown {
		known = 1
	}
	if state == CheckAbandoned && known == 0 {
		cost = CheckCapMicro // an abandoned check's reviewer call may have been paid: it counts at the cap, not as free
	}
	return h.s.tx(ctx, func(x tasks.Queryer) error {
		var goal, st string
		if err := x.QueryRowContext(ctx, `SELECT goal_hash, state FROM mission_goal_checks WHERE id = ?`, checkID).Scan(&goal, &st); err != nil {
			if err == sql.ErrNoRows {
				return ErrNoCheck
			}
			return err
		}
		if st != CheckRunning {
			return invalid("check #%d has ended already", checkID)
		}
		if res.GoalHash != goal {
			return invalid("the result is for another goal than check #%d", checkID)
		}
		_, err := x.ExecContext(ctx, `UPDATE mission_goal_checks SET state = ?, outcome = ?, reason = ?, subject_digest = ?, subject_files = ?, containment = ?, net = ?,
		  reviewer_pin = ?, reviewer_observed = ?, reviewer_verdict = ?, reviewer_reason = ?, results_digest = ?, cost_micro = ?, cost_known = ?, ended_at = ?
		  WHERE id = ? AND state = 'running'`,
			state, outcome, Clean(string(res.Reason), 200), Clean(res.SubjectDigest, 200), max(res.SubjectFiles, 0), Clean(res.Containment, 200), Clean(res.Net, 200),
			Clean(res.ReviewerPin, 200), Clean(res.ReviewerObserved, 200), res.ReviewerVerdict, Clean(res.ReviewerReason, 200), Clean(res.ResultsDigest, 200), cost, known, h.s.stamp(), checkID)
		if err != nil {
			return fmt.Errorf("missionpursuit: ending a goal check: %w", err)
		}
		return nil
	})
}

// AbandonRunning ends every running check as abandoned (a restart of the host: the check that was running is gone), and
// says how many. An abandoned check still counts against the caps.
func (h HostChecks) AbandonRunning(ctx context.Context) (int, error) {
	if h.s == nil {
		return 0, errNoHostChecks
	}
	res, err := h.s.db.ExecContext(ctx, `UPDATE mission_goal_checks SET state = 'abandoned', outcome = '', reason = 'host-restarted', cost_micro = ?, cost_known = 0, ended_at = ? WHERE state = 'running'`, CheckCapMicro, h.s.stamp())
	if err != nil {
		return 0, fmt.Errorf("missionpursuit: abandoning running checks: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
