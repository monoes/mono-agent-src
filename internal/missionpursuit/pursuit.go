package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The away limits: how far a pursuit runs while the person is away.
const (
	DefaultParkedMax = 3  // parked questions since the person last acted
	MaxParkedMax     = 20 // the most the person can allow
	DefaultPct       = 20 // share of the budget spent since then
	MaxPct           = 100
	DefaultWakesMax  = 6 // wake-ups of the lead queued since then (a count, so it does not depend on the meter)
	MaxWakesMax      = 50
)

// AwayLimits are the two away limits of a pursuit. A zero field means its default.
type AwayLimits struct {
	ParkedMax int `json:"parked_max"`
	Pct       int `json:"pct"`
	Wakes     int `json:"wakes"` // wake-ups of the lead since the person last acted
}

// resolve fills the defaults and refuses a value outside 1..max.
func (l AwayLimits) resolve() (AwayLimits, error) {
	if l.ParkedMax == 0 {
		l.ParkedMax = DefaultParkedMax
	}
	if l.Pct == 0 {
		l.Pct = DefaultPct
	}
	if l.Wakes == 0 {
		l.Wakes = DefaultWakesMax
	}
	if l.Wakes < 1 || l.Wakes > MaxWakesMax {
		return l, invalid("the away limit on wake-ups is 1 to %d (got %d)", MaxWakesMax, l.Wakes)
	}
	if l.ParkedMax < 1 || l.ParkedMax > MaxParkedMax {
		return l, invalid("the away limit on parked questions is 1 to %d (got %d)", MaxParkedMax, l.ParkedMax)
	}
	if l.Pct < 1 || l.Pct > MaxPct {
		return l, invalid("the away limit on spend is 1 to %d percent (got %d)", MaxPct, l.Pct)
	}
	return l, nil
}

// A Pursuit is the policy record of a mission's pursuit. It runs nothing.
type Pursuit struct {
	SessionID int64  `json:"session_id"`
	Enabled   bool   `json:"enabled"`
	ParkedMax int    `json:"away_budget_parked_max"`
	Pct       int    `json:"away_budget_pct"`
	WakesMax  int    `json:"away_wakes_max"`
	PausedBy  string `json:"paused_by,omitempty"`
	LockedBy  string `json:"locked_by,omitempty"`
	// UnconfinedChecksOK is the person's switch (default off): goal checks may run project commands without the confinement the
	// verifier proves. UnconfinedBy is who last changed it.
	UnconfinedChecksOK bool   `json:"unconfined_checks_ok"`
	UnconfinedBy       string `json:"unconfined_by,omitempty"`
	updatedAt          string
}

// Paused says whether the person paused the pursuit.
func (p Pursuit) Paused() bool { return p.PausedBy != "" }

var (
	// ErrAlreadyStarted: the mission is in pursuit already.
	ErrAlreadyStarted = fmt.Errorf("%w: this mission's pursuit is already started", tasks.ErrInvalid)
	// ErrNotStarted: the mission has no pursuit.
	ErrNotStarted = fmt.Errorf("%w: this mission's pursuit is not started", tasks.ErrNotFound)
)

// Start begins the pursuit of a mission whose goal the person confirmed, and locks the goal with it, in one transaction:
// either both happen or neither. Only the operator may. It is the one caller of the lock.
func (s *Store) Start(ctx context.Context, profileID string, session int64, limits AwayLimits, by tasks.Actor) (Pursuit, error) {
	if by.Kind != tasks.Human {
		return Pursuit{}, operatorOnly("start a mission's pursuit")
	}
	lim, err := limits.resolve()
	if err != nil {
		return Pursuit{}, err
	}
	var out Pursuit
	err = s.tx(ctx, func(x tasks.Queryer) error {
		if _, err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		if _, err := readPursuit(ctx, x, session); err == nil {
			return ErrAlreadyStarted
		} else if !errors.Is(err, ErrNotStarted) {
			return err
		}
		g, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if g.State != StateConfirmed {
			if g.State == StateProposed {
				return ErrNotConfirmed
			}
			return ErrGoalLocked
		}
		if !hasRubric(g.Criteria) {
			return invalid("a mission needs at least one rubric criterion before it can be pursued: you are always the last judge, so state what you will judge yourself")
		}
		now := s.stamp()
		if _, err := x.ExecContext(ctx, `INSERT INTO mission_pursuit (session_id, enabled, away_budget_parked_max, away_budget_pct, away_wakes_max, locked_by, created_at, updated_at) VALUES (?, 1, ?, ?, ?, ?, ?, ?)`,
			session, lim.ParkedMax, lim.Pct, lim.Wakes, by.Label(), now, now); err != nil {
			return fmt.Errorf("missionpursuit: starting: %w", err)
		}
		if _, err := s.lockIn(ctx, x, profileID, session); err != nil {
			return err
		}
		out, err = readPursuit(ctx, x, session)
		return err
	})
	return out, err
}

// hasRubric says whether a criterion is one the person decides.
func hasRubric(cs []StoredCriterion) bool {
	for _, c := range cs {
		if c.Kind == KindRubric {
			return true
		}
	}
	return false
}

// Pause stops the pursuit from opening new work until the person resumes it. Only the operator may.
func (s *Store) Pause(ctx context.Context, profileID string, session int64, by tasks.Actor) (Pursuit, error) {
	if by.Kind != tasks.Human {
		return Pursuit{}, operatorOnly("pause a mission's pursuit")
	}
	return s.setPaused(ctx, profileID, session, by.Label(), true)
}

// Resume lets a paused pursuit go on. Only the operator may.
func (s *Store) Resume(ctx context.Context, profileID string, session int64, by tasks.Actor) (Pursuit, error) {
	if by.Kind != tasks.Human {
		return Pursuit{}, operatorOnly("resume a mission's pursuit")
	}
	return s.setPaused(ctx, profileID, session, "", false)
}

func (s *Store) setPaused(ctx context.Context, profileID string, session int64, who string, pause bool) (Pursuit, error) {
	var out Pursuit
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if _, err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		cur, err := readPursuit(ctx, x, session)
		if err != nil {
			return err
		}
		if pause == cur.Paused() {
			if pause {
				return invalid("this mission's pursuit is paused already")
			}
			return invalid("this mission's pursuit is not paused")
		}
		if _, err := x.ExecContext(ctx, `UPDATE mission_pursuit SET paused_by = ?, updated_at = ? WHERE session_id = ?`, who, s.stamp(), session); err != nil {
			return fmt.Errorf("missionpursuit: pausing or resuming: %w", err)
		}
		out, err = readPursuit(ctx, x, session)
		return err
	})
	return out, err
}

// GetPursuit is the pursuit record of a mission session of the profile; tasks.ErrNotFound when it has none.
func (s *Store) GetPursuit(ctx context.Context, profileID string, session int64) (Pursuit, error) {
	var out Pursuit
	err := s.read(ctx, func(x tasks.Queryer) error {
		var id int64
		if err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&id); err != nil {
			return notFound(err, session)
		}
		p, err := readPursuit(ctx, x, session)
		out = p
		return err
	})
	return out, err
}

func readPursuit(ctx context.Context, x tasks.Queryer, session int64) (Pursuit, error) {
	p := Pursuit{SessionID: session}
	var enabled, unconfined int
	err := x.QueryRowContext(ctx, `SELECT enabled, away_budget_parked_max, away_budget_pct, away_wakes_max, paused_by, locked_by, unconfined_checks_ok, unconfined_by, updated_at FROM mission_pursuit WHERE session_id = ?`, session).
		Scan(&enabled, &p.ParkedMax, &p.Pct, &p.WakesMax, &p.PausedBy, &p.LockedBy, &unconfined, &p.UnconfinedBy, &p.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Pursuit{}, ErrNotStarted
	}
	if err != nil {
		return Pursuit{}, fmt.Errorf("missionpursuit: reading the pursuit: %w", err)
	}
	p.Enabled = enabled == 1
	p.UnconfinedChecksOK = unconfined == 1
	return p, nil
}
