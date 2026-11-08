package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/judge"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
	"github.com/monoes/mono-agent/internal/missionpursuit/scorer"
)

// The scorer's Facts, read from host rows only (the R-list of scorer/doc.go):
//   - the lead session of a direction is parsed strictly, must be a 'direction' session of THIS mission and profile, and
//     must belong to one open direction only;
//   - a task is done only when the host verified it (verified = 1); a task an agent moved to done is still open;
//   - failed = archived tasks and tasks blocked after failed verification (retries > 0); blocked = blocked with none;
//     every task is in exactly one bucket; epics are not work;
//   - criteria are counted against the whole confirmed goal; no host-run criterion check exists yet, so none passed;
//   - evidence is the artifact rows of verified tasks; spend is the direction session's own meter against its own budget.
// One transaction per direction, so the numbers come from one snapshot of the database.

type pursuitFacts struct {
	db       *sql.DB
	profile  string
	mission  int64
	criteria int
	uses     map[string]int // lead session -> open directions naming it
}

func newPursuitFacts(db *sql.DB, profile string, mission int64, criteria int, dirs []types.Direction) *pursuitFacts {
	f := &pursuitFacts{db: db, profile: profile, mission: mission, criteria: criteria, uses: map[string]int{}}
	for _, d := range dirs {
		if d.SessionID != "" {
			f.uses[d.SessionID]++
		}
	}
	return f
}

// pursuitRead is what is known of one direction: the scorer's facts and the activity the epoch engine reads.
type pursuitRead struct {
	Facts        scorer.DirectionFacts
	Finished     bool
	LastProgress time.Time
}

// plainID is a positive decimal id with no sign, space or leading zero.
func plainID(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != s {
		return 0, errors.New("the session id is not a plain positive integer")
	}
	return n, nil
}

var errPursuitFacts = errors.New("the direction's facts cannot be read")

// DirectionFacts implements scorer.Facts.
func (f *pursuitFacts) DirectionFacts(ctx context.Context, sessionID string) (scorer.DirectionFacts, error) {
	r, err := f.read(ctx, sessionID)
	if err != nil {
		return scorer.DirectionFacts{}, err
	}
	if x := r.Facts; x.TasksDone+x.TasksFailed+x.TasksOpen+x.TasksBlocked+x.EvidenceRows == 0 {
		return scorer.DirectionFacts{}, fmt.Errorf("%w: no host-observed activity", errPursuitFacts) // an unmeasured direction is unscored
	}
	return r.Facts, nil
}

func (f *pursuitFacts) read(ctx context.Context, sessionID string) (pursuitRead, error) {
	var out pursuitRead
	id, err := plainID(sessionID)
	if err != nil {
		return out, err
	}
	if f.uses[sessionID] > 1 {
		return out, fmt.Errorf("%w: two directions share the session", errPursuitFacts)
	}
	tx, err := f.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback() //nolint:errcheck // read only
	var state, kind, profile string
	var parent sql.NullInt64
	var budget, spent float64
	err = tx.QueryRowContext(ctx, `SELECT state, kind, profile_id, parent_session_id, budget_usd, spent_usd FROM task_sessions WHERE id = ? AND archived_at = ''`, id).
		Scan(&state, &kind, &profile, &parent, &budget, &spent)
	if err != nil {
		return out, fmt.Errorf("%w: %v", errPursuitFacts, err)
	}
	if kind != "direction" || profile != f.profile || !parent.Valid || parent.Int64 != f.mission {
		return out, fmt.Errorf("%w: not a direction session of this mission", errPursuitFacts)
	}
	rows, err := tx.QueryContext(ctx, `SELECT status, blocked, verified, retries, COUNT(*), MAX(updated_at) FROM tasks WHERE session_id = ? AND kind <> 'epic' GROUP BY status, blocked, verified, retries`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var status, upd string
		var blocked, verified, retries, n int
		if err := rows.Scan(&status, &blocked, &verified, &retries, &n, &upd); err != nil {
			return out, err
		}
		switch {
		case status == "done" && verified == 1:
			out.Facts.TasksDone += n
		case status == "archived", status != "done" && blocked == 1 && retries > 0:
			out.Facts.TasksFailed += n
		case status != "done" && blocked == 1:
			out.Facts.TasksBlocked += n
		default:
			out.Facts.TasksOpen += n
		}
		if t, err := time.Parse(time.RFC3339Nano, upd); err == nil && t.After(out.LastProgress) {
			out.LastProgress = t
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_artifacts a JOIN tasks t ON t.id = a.task_id WHERE t.session_id = ? AND t.kind <> 'epic' AND t.status = 'done' AND t.verified = 1`, id).
		Scan(&out.Facts.EvidenceRows); err != nil {
		return out, err
	}
	out.Facts.CriteriaChecked = f.criteria
	if budget > 0 && spent >= 0 && budget < 1e300 && spent < 1e300 {
		out.Facts.SpendKnown = true
		out.Facts.SpendPermille = int(min(spent/budget*1000, 1e6))
	}
	out.Finished = state == "done" || state == "stopped" || state == "failed"
	return out, nil
}

// goalCriteria are the confirmed goal's criteria as the judge sees them (the person confirmed them; they are still data).
func goalCriteria(g missionpursuit.Goal) []judge.CriterionInput {
	out := make([]judge.CriterionInput, 0, len(g.Criteria))
	for _, c := range g.Criteria {
		out = append(out, judge.CriterionInput{ID: c.ID, Text: c.Spec})
	}
	return out
}

// pursuitInputs implements epoch.Inputs for one mission over inputs.Reader.
type pursuitInputs struct {
	db       *sql.DB
	rd       *inputs.Reader
	profile  string
	mission  int64
	nCrit    int
	criteria []judge.CriterionInput
	clock    epoch.Clock
	gap      time.Duration

	mu       sync.Mutex
	shown    int64
	shownAt  time.Time
	haveSeen bool
}

func newPursuitInputs(db *sql.DB, profile string, mission int64, nCrit int, criteria []judge.CriterionInput, clock epoch.Clock, gap time.Duration) *pursuitInputs {
	return &pursuitInputs{db: db, rd: inputs.New(db), profile: profile, mission: mission, nCrit: nCrit, criteria: criteria, clock: clock, gap: gap}
}

var errPursuitStopped = errors.New("the pursuit is not running")

// Snapshot is the epoch engine's view; a pursuit that does not run is an error, so the engine does nothing.
func (p *pursuitInputs) Snapshot(ctx context.Context) (epoch.Snapshot, error) {
	s, err := p.rd.Snapshot(ctx, p.mission)
	if err != nil {
		return epoch.Snapshot{}, err
	}
	if !s.Run.Running {
		return epoch.Snapshot{}, errPursuitStopped
	}
	out := epoch.Snapshot{
		Criteria:    p.criteria,
		Summaries:   map[string]string{},
		Budget:      epoch.BudgetShare{Known: s.Budget.Known, Share: s.Budget.Share},
		EvidenceSeq: p.evidence(s.EvidenceSeq),
		FinishParks: s.Epochs.ParkedFinishRounds,
	}
	facts := newPursuitFacts(p.db, p.profile, p.mission, p.nCrit, s.Directions)
	for _, d := range s.Directions {
		if d.SessionID == "" {
			continue
		}
		r, err := facts.read(ctx, d.SessionID)
		if err != nil {
			continue // unreadable: no summary, no activity, never stalled
		}
		f := r.Facts
		out.Summaries[d.ID] = fmt.Sprintf("%d done, %d failed, %d open, %d blocked", f.TasksDone, f.TasksFailed, f.TasksOpen, f.TasksBlocked)
		out.Activity = append(out.Activity, epoch.DirectionActivity{ID: d.ID, LastProgress: r.LastProgress, Finished: r.Finished})
	}
	return out, nil
}

// evidence rate-limits the new-evidence trigger: agents can add artifact rows at will, so a growth is shown to the engine
// at most once per gap, the first sight is the baseline, and a drop never lowers it.
func (p *pursuitInputs) evidence(actual int64) int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.clock.Now()
	switch {
	case !p.haveSeen:
		p.shown, p.shownAt, p.haveSeen = actual, now, true
	case actual > p.shown && now.Sub(p.shownAt) >= p.gap:
		p.shown, p.shownAt = actual, now
	}
	return p.shown
}
