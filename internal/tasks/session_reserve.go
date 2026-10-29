package tasks

import (
	"context"
	"fmt"
)

// minReserve is the least a worker is issued: below it the session is out of budget.
const minReserve = 0.01

// reserveActor is the operator's process, which alone records spend and the caps it issues.
var reserveActor = Actor{Kind: Human}

// ReserveSpend issues a worker of the session a cap out of what the session has left: for a session
// with a budget the grant is the smaller of want and what is left (want <= 0: all that is left),
// refused below $0.01, and it counts against the session (reserved_usd) until SettleSpend. A session
// without a budget grants want unchanged. The check and the write are one immediate transaction, so a
// carve or another reservation cannot take the same money.
func (s *Store) ReserveSpend(ctx context.Context, profileID string, sessionID int64, want float64) (float64, error) {
	if !finite(want) {
		return 0, invalid("the amount to reserve is a number of dollars")
	}
	var granted float64
	_, err := s.sessionWrite(ctx, profileID, sessionID, func(x dbx, se Session) error {
		if !isOpen(se.State) {
			return sessionNotReady("session #%d is %s: it issues no more budget to workers", se.ID, closedWord(se))
		}
		if se.BudgetUSD <= 0 {
			granted = max(want, 0)
			return nil
		}
		left, held, err := sessionLeft(ctx, x, se)
		if err != nil {
			return err
		}
		granted = left
		if want > 0 {
			granted = min(want, left)
		}
		if granted < minReserve {
			return limit("session #%d has no budget left to reserve ($%.2f left of $%.2f: $%.2f spent, $%.2f reserved by running workers, $%.2f held by its children)", se.ID, max(left, 0), se.BudgetUSD, se.SpentUSD, se.ReservedUSD, held)
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET reserved_usd = reserved_usd + ? WHERE id = ?`, granted, se.ID); err != nil {
			return fmt.Errorf("tasks: reserving budget of session #%d: %w", se.ID, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return granted, nil
}

// SettleSpend ends a reservation: reserved comes off what the session's workers were issued and actual
// goes on what it spent (actual may be 0, and may exceed reserved: an overspend cannot be prevented, it
// is recorded as it happened and leaves nothing to reserve, carve or dispatch).
func (s *Store) SettleSpend(ctx context.Context, profileID string, sessionID int64, reserved, actual float64, note string) error {
	if !finite(reserved) || !finite(actual) || reserved < 0 || actual < 0 {
		return invalid("reserved and actual are amounts in dollars, 0 or more")
	}
	_, err := s.sessionWrite(ctx, profileID, sessionID, func(x dbx, se Session) error {
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET reserved_usd = MAX(reserved_usd - ?, 0) WHERE id = ?`, reserved, se.ID); err != nil {
			return fmt.Errorf("tasks: settling budget of session #%d: %w", se.ID, err)
		}
		if actual > 0 {
			return s.applySpend(ctx, x, se.ID, actual, note, reserveActor)
		}
		return nil
	})
	return err
}

// ReleaseReserved gives back every reservation of a session whose workers are gone (a crashed or
// cancelled run that never settled). The caller is the process that runs the workers and knows none is
// alive for the session.
func (s *Store) ReleaseReserved(ctx context.Context, profileID string, sessionID int64) error {
	_, err := s.sessionWrite(ctx, profileID, sessionID, func(x dbx, se Session) error {
		if se.ReservedUSD == 0 {
			return nil
		}
		if _, err := x.ExecContext(ctx, `UPDATE task_sessions SET reserved_usd = 0 WHERE id = ?`, se.ID); err != nil {
			return fmt.Errorf("tasks: releasing the reservations of session #%d: %w", se.ID, err)
		}
		return s.sessionEvent(ctx, x, se.ID, reserveActor, "reserve_released", fmt.Sprintf("$%.4f of reservations of workers that are gone", se.ReservedUSD))
	})
	return err
}
