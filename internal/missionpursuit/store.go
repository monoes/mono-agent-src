package missionpursuit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The states of a goal.
const (
	StateProposed  = "proposed"
	StateConfirmed = "confirmed"
	StateLocked    = "locked"
)

var (
	// ErrGoalLocked: the goal is confirmed or locked. A changed goal means a new mission.
	ErrGoalLocked = fmt.Errorf("%w: the mission goal is confirmed and final: a changed goal means a new mission", tasks.ErrInvalid)
	// ErrStale: the criteria are not the ones the person saw.
	ErrStale = fmt.Errorf("%w: the proposed criteria changed since you read them: read them again before you confirm", tasks.ErrInvalid)
)

// A StoredCriterion is a criterion of a goal, with its id (c1, c2, ...) and its hash.
type StoredCriterion struct {
	ID   string `json:"id"`
	Hash string `json:"hash"`
	Criterion
}

// A Goal is the ultimate goal record of a mission session.
type Goal struct {
	SessionID   int64             `json:"session_id"`
	State       string            `json:"state"`
	Hash        string            `json:"hash"`
	ProposedBy  string            `json:"proposed_by"`
	ConfirmedBy string            `json:"confirmed_by,omitempty"`
	ConfirmedAt string            `json:"confirmed_at,omitempty"`
	LockedAt    string            `json:"locked_at,omitempty"`
	Criteria    []StoredCriterion `json:"criteria"`
}

// Store reads and writes mission goals.
type Store struct {
	db    *sql.DB
	modes ModeReader
	now   func() time.Time
}

// NewStore is a store whose chat modes come from coder_sessions.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db, modes: CoderSessionModes{}, now: time.Now}
}

// WithModes is a copy of the store that reads chat modes from m.
func (s *Store) WithModes(m ModeReader) *Store {
	c := *s
	c.modes = m
	return &c
}

func (s *Store) stamp() string { return s.now().UTC().Format(time.RFC3339) }

// Propose records the criteria as a proposal, or replaces the earlier proposal with them: an agent or the operator may,
// until the person confirms. It never confirms anything.
func (s *Store) Propose(ctx context.Context, profileID string, session int64, cs []Criterion, by tasks.Actor) (Goal, error) {
	if by.Kind != tasks.Human && by.Kind != tasks.Agent {
		return Goal{}, operatorOnly("propose a mission goal")
	}
	if by.Session != 0 && by.Session != session {
		return Goal{}, operatorOnly("propose the goal of a mission this agent does not work in")
	}
	return s.write(ctx, profileID, session, cs, by, false)
}

// Set records the criteria and confirms them in one step. Only the operator may. A confirmed goal is final.
func (s *Store) Set(ctx context.Context, profileID string, session int64, cs []Criterion, by tasks.Actor) (Goal, error) {
	if by.Kind != tasks.Human {
		return Goal{}, operatorOnly("set a mission goal")
	}
	return s.write(ctx, profileID, session, cs, by, true)
}

// write validates and records a proposal, and confirms it when confirm is set.
func (s *Store) write(ctx context.Context, profileID string, session int64, cs []Criterion, by tasks.Actor, confirm bool) (Goal, error) {
	if err := Validate(cs); err != nil {
		return Goal{}, err
	}
	var out Goal
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		cur, err := readGoal(ctx, x, session)
		if err != nil && !errors.Is(err, tasks.ErrNotFound) {
			return err
		}
		if err == nil && cur.State != StateProposed {
			return ErrGoalLocked
		}
		if err := s.writeProposal(ctx, x, session, cs, by.Label(), err == nil); err != nil {
			return err
		}
		if confirm {
			if err := s.confirm(ctx, x, session, by); err != nil {
				return err
			}
		}
		out, err = readGoal(ctx, x, session)
		return err
	})
	return out, err
}

// writeProposal writes the proposal row and its criteria; revise says a proposal row exists to replace.
func (s *Store) writeProposal(ctx context.Context, x tasks.Queryer, session int64, cs []Criterion, by string, revise bool) error {
	now := s.stamp()
	if revise {
		if _, err := x.ExecContext(ctx, `UPDATE mission_goals SET criteria_hash = ?, proposed_by = ?, updated_at = ? WHERE session_id = ? AND state = 'proposed'`, Hash(cs), by, now, session); err != nil {
			return fmt.Errorf("missionpursuit: revising the proposal: %w", err)
		}
		if _, err := x.ExecContext(ctx, `DELETE FROM mission_goal_criteria WHERE session_id = ?`, session); err != nil {
			return fmt.Errorf("missionpursuit: revising the proposal: %w", err)
		}
	} else if _, err := x.ExecContext(ctx, `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (?, 'proposed', ?, ?, ?, ?)`, session, Hash(cs), by, now, now); err != nil {
		return fmt.Errorf("missionpursuit: recording the proposal: %w", err)
	}
	for i, c := range cs {
		if _, err := x.ExecContext(ctx, `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, expected, hash) VALUES (?, ?, ?, ?, ?, ?)`, session, i+1, c.Kind, c.Spec, c.Expected, criterionHash(c)); err != nil {
			return fmt.Errorf("missionpursuit: recording a criterion: %w", err)
		}
	}
	return nil
}

// Confirm makes the proposal final, if its criteria hash is wantHash: the hash of what the person saw. Only the operator may.
func (s *Store) Confirm(ctx context.Context, profileID string, session int64, wantHash string, by tasks.Actor) (Goal, error) {
	if by.Kind != tasks.Human {
		return Goal{}, operatorOnly("confirm a mission goal")
	}
	var out Goal
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		cur, err := readGoal(ctx, x, session)
		if err != nil {
			return err
		}
		if cur.State != StateProposed {
			return ErrGoalLocked
		}
		if wantHash == "" || wantHash != cur.Hash {
			return ErrStale
		}
		if err := s.confirm(ctx, x, session, by); err != nil {
			return err
		}
		out, err = readGoal(ctx, x, session)
		return err
	})
	return out, err
}

// confirm moves the proposal to confirmed; the hash it holds is the one readGoal has just checked against the rows.
func (s *Store) confirm(ctx context.Context, x tasks.Queryer, session int64, by tasks.Actor) error {
	now := s.stamp()
	res, err := x.ExecContext(ctx, `UPDATE mission_goals SET state = 'confirmed', confirmed_by = ?, confirmed_at = ?, updated_at = ? WHERE session_id = ? AND state = 'proposed'`, by.Label(), now, now, session)
	if err != nil {
		return fmt.Errorf("missionpursuit: confirming: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrGoalLocked
	}
	return nil
}

// Lock makes a confirmed goal terminal. Only the operator may. Nothing calls it yet: the pursuit that will lock the goal comes later.
func (s *Store) Lock(ctx context.Context, profileID string, session int64, by tasks.Actor) (Goal, error) {
	if by.Kind != tasks.Human {
		return Goal{}, operatorOnly("lock a mission goal")
	}
	var out Goal
	err := s.tx(ctx, func(x tasks.Queryer) error {
		if err := s.checkMission(ctx, x, profileID, session); err != nil {
			return err
		}
		now := s.stamp()
		res, err := x.ExecContext(ctx, `UPDATE mission_goals SET state = 'locked', locked_at = ?, updated_at = ? WHERE session_id = ? AND state = 'confirmed'`, now, now, session)
		if err != nil {
			return fmt.Errorf("missionpursuit: locking: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			if _, rerr := readGoal(ctx, x, session); rerr != nil {
				return rerr
			}
			return ErrGoalLocked
		}
		out, err = readGoal(ctx, x, session)
		return err
	})
	return out, err
}

// Get is the goal of a mission session of the profile; tasks.ErrNotFound when there is none.
func (s *Store) Get(ctx context.Context, profileID string, session int64) (Goal, error) {
	var out Goal
	err := s.read(ctx, func(x tasks.Queryer) error {
		var id int64
		if err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE id = ? AND profile_id = ?`, session, profileID).Scan(&id); err != nil {
			return notFound(err, session)
		}
		g, err := readGoal(ctx, x, session)
		out = g
		return err
	})
	return out, err
}

// ChatIsMission says whether the conversation's chat is in mission mode. A mode that cannot be read is not mission.
func (s *Store) ChatIsMission(ctx context.Context, profileID, conversationID string) bool {
	var is bool
	err := s.read(ctx, func(x tasks.Queryer) error {
		mode, err := s.modes.ChatMode(ctx, x, profileID, conversationID)
		is = err == nil && mode == ChatModeMission
		return err
	})
	return err == nil && is
}

func notFound(err error, session int64) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: no session #%d in this profile", tasks.ErrNotFound, session)
	}
	return err
}

func operatorOnly(what string) error {
	return fmt.Errorf("%w: %s", tasks.ErrOperatorOnly, what)
}
