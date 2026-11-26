package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The wrap-up allowance of a mission (#1751, the owner's decision): a mission that has spent its budget does not
// refuse its lead. The lead may still spend WrapUpAllowanceUSD on its own turns to wrap up or finish the work, once
// per exhaustion; workers get nothing. A turn holds all that is left from its start until its cost is known, so a
// turn that dies or reports no cost keeps the hold and two turns are never given the same money. Raising the budget
// gives the next exhaustion a new allowance: a turn counts only against budgets up to the one it ran under.

// WrapUpAllowanceUSD is what the lead may spend on its own turns after the budget is spent.
const WrapUpAllowanceUSD = 5.0

// ConductorName is the name the mission scheduler acts under. A pause written under it is the scheduler's budget
// halt, never the operator's pause.
const ConductorName = "conductor"

// ErrWrapUpUsedUp: nothing is left of the allowance for another turn.
var ErrWrapUpUsedUp = errors.New("the mission's wrap-up allowance is used up")

// WrapUp is the allowance of a mission that has spent its budget.
type WrapUp struct {
	AllowanceUSD float64 `json:"allowance_usd"`
	SpentUSD     float64 `json:"spent_usd"` // what the lead's wrap-up turns cost
	HeldUSD      float64 `json:"held_usd"`  // held by turns whose cost is not known: running, or ended without one
	LeftUSD      float64 `json:"left_usd"`  // what a new turn may use
}

// Lasts says whether the turns so far have left any of the allowance: the lead's hand-back tools work while it does,
// including inside the turn that holds the rest.
func (w WrapUp) Lasts() bool { return toMicros(w.SpentUSD) < toMicros(w.AllowanceUSD) }

// wrapUpTx is the allowance of se, or nil when none applies: only a session with a budget it has spent, running or
// halted by the scheduler. An operator's pause or stop, a draft and a closed session have none.
func (s *Store) wrapUpTx(ctx context.Context, x dbx, se Session) (*WrapUp, error) {
	if se.Archived || se.BudgetUSD <= 0 || toMicros(se.SpentUSD) < toMicros(se.BudgetUSD) {
		return nil, nil
	}
	switch se.State {
	case SessionRunning:
	case SessionPaused:
		var by string
		if err := x.QueryRowContext(ctx, `SELECT COALESCE((SELECT actor FROM task_session_events WHERE session_id = ? AND kind IN ('pause', 'resume')
			ORDER BY id DESC LIMIT 1), '')`, se.ID).Scan(&by); err != nil {
			return nil, fmt.Errorf("tasks: reading who paused session #%d: %w", se.ID, err)
		}
		if by != ConductorName {
			return nil, nil
		}
	default:
		return nil, nil
	}
	var spent, held int64
	if err := x.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN settled = 1 THEN spent_micros ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN settled = 0 THEN hold_micros ELSE 0 END), 0)
		FROM task_session_wrapup_turns WHERE session_id = ? AND budget_micros >= ?`, se.ID, toMicros(se.BudgetUSD)).Scan(&spent, &held); err != nil {
		return nil, fmt.Errorf("tasks: reading the wrap-up allowance of session #%d: %w", se.ID, err)
	}
	left := max(toMicros(WrapUpAllowanceUSD)-spent-held, 0)
	return &WrapUp{AllowanceUSD: WrapUpAllowanceUSD, SpentUSD: fromMicros(spent), HeldUSD: fromMicros(held), LeftUSD: fromMicros(left)}, nil
}

// handBackBy is writableBy for the writes that hand a task back (its evidence and its finish): the lead of a mission
// that has spent its budget may still make them while its wrap-up allowance lasts (Actor.WrapUp).
func (s *Store) handBackBy(ctx context.Context, x dbx, se Session, actor Actor, what string) error {
	if actor.WrapUp {
		w, err := s.wrapUpTx(ctx, x, se)
		if err != nil {
			return err
		}
		if w != nil && w.Lasts() {
			return nil
		}
	}
	return writableBy(se, actor, what)
}

// WrapUpOf is the wrap-up allowance of a session as it stands, nil when none applies.
func (s *Store) WrapUpOf(ctx context.Context, profileID string, id int64, actor Actor) (*WrapUp, error) {
	if err := readerOf(actor, id); err != nil {
		return nil, err
	}
	var out *WrapUp
	err := s.snapshot(ctx, func(x dbx) error {
		se, err := s.sessionTx(ctx, x, profileID, id)
		if err != nil {
			return err
		}
		out, err = s.wrapUpTx(ctx, x, se)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// BeginWrapUpTurn admits a turn of the lead under the allowance and holds all that is left for it. It returns the
// allowance as it was before the hold (LeftUSD is what the turn may use), nil when no allowance applies, and
// ErrWrapUpUsedUp (with the allowance) when nothing is left. Only the operator's process records it.
func (s *Store) BeginWrapUpTurn(ctx context.Context, profileID string, id int64, turnID string, actor Actor) (*WrapUp, error) {
	if actor.Kind != Human {
		return nil, operatorOnly("admit a turn under a mission's wrap-up allowance")
	}
	if strings.TrimSpace(turnID) == "" {
		return nil, invalid("a wrap-up turn needs the turn's id")
	}
	// Most turns have no allowance: they are answered without taking the write lock.
	if w, err := s.WrapUpOf(ctx, profileID, id, actor); err != nil || w == nil {
		return nil, err
	}
	var out *WrapUp
	_, err := s.sessionWriteIf(ctx, profileID, id, func(x dbx, se Session) (bool, error) {
		w, err := s.wrapUpTx(ctx, x, se)
		if out = w; err != nil || w == nil {
			return false, err
		}
		if w.LeftUSD <= 0 {
			return false, ErrWrapUpUsedUp
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO task_session_wrapup_turns (profile_id, session_id, turn_id, budget_micros, hold_micros, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, profileID, id, turnID, toMicros(se.BudgetUSD), toMicros(w.LeftUSD), s.stamp()); err != nil {
			return false, fmt.Errorf("tasks: holding the wrap-up allowance of session #%d: %w", id, err)
		}
		return true, nil
	})
	if err != nil && !errors.Is(err, ErrWrapUpUsedUp) {
		return nil, err
	}
	return out, err
}

// SettleWrapUpTurn records what an admitted turn cost, in place of its hold, and adds it to the session's spend,
// both in one write and once: a second report of the same turn changes nothing. A session that was stopped or closed
// meanwhile is not charged, as for any turn of the lead.
func (s *Store) SettleWrapUpTurn(ctx context.Context, profileID string, id int64, turnID string, usd float64, actor Actor) error {
	if actor.Kind != Human {
		return operatorOnly("record the cost of a wrap-up turn")
	}
	if !finite(usd) || usd < 0 || usd > MaxBudgetUSD {
		return invalid("the cost is an amount in dollars, 0 to %.0f", MaxBudgetUSD)
	}
	_, err := s.sessionWriteIf(ctx, profileID, id, func(x dbx, se Session) (bool, error) {
		res, err := x.ExecContext(ctx, `UPDATE task_session_wrapup_turns SET spent_micros = ?, settled = 1
			WHERE profile_id = ? AND session_id = ? AND turn_id = ? AND settled = 0`, toMicros(usd), profileID, id, turnID)
		if err != nil {
			return false, fmt.Errorf("tasks: settling a wrap-up turn of session #%d: %w", id, err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return false, err
		}
		if usd == 0 || se.Archived || (se.State != SessionRunning && se.State != SessionPaused) {
			return true, nil
		}
		return true, s.applySpend(ctx, x, id, usd, "the lead's wrap-up turn", actor)
	})
	return err
}

// DropWrapUpTurn gives back the hold of a turn that never ran. A settled turn stays.
func (s *Store) DropWrapUpTurn(ctx context.Context, profileID string, id int64, turnID string, actor Actor) error {
	if actor.Kind != Human {
		return operatorOnly("give back a wrap-up turn's hold")
	}
	_, err := s.sessionWriteIf(ctx, profileID, id, func(x dbx, se Session) (bool, error) {
		res, err := x.ExecContext(ctx, `DELETE FROM task_session_wrapup_turns WHERE profile_id = ? AND session_id = ? AND turn_id = ? AND settled = 0`, profileID, id, turnID)
		if err != nil {
			return false, fmt.Errorf("tasks: dropping a wrap-up turn of session #%d: %w", id, err)
		}
		n, err := res.RowsAffected()
		return n > 0, err
	})
	return err
}
