package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// A session's workers are accounted in ONE mode at a time (migration 157): as reservations, which hold money out of the
// budget, or on the session's cap, which holds nothing (the run reads what is left and its cost is added to the spend
// as it streams). A cap run's dollars are not held, so a reservation granted beside it could take the same ones. A
// cap run therefore registers here while it runs, and the two kinds are refused each other's session inside the
// write transaction of the call that would mix them. The mode is "drained" when the other kind has no live row; a
// row whose lease lapsed by more than ReservationGrace is dead and counts for nothing.

// ErrAccountingMode says the session's workers are accounted the other way right now: the caller follows the mode
// that is in flight (or waits for the drain).
var ErrAccountingMode = errors.New("the session is accounted in the other mode while its other runs are in flight")

// CapRun is a registered run that is accounted on the session's cap. It holds no money.
type CapRun struct {
	ID         int64
	ProfileID  string
	SessionID  int64
	OwnerID    string
	RunRef     string
	LeaseUntil string
}

// MaxCapRunsPerSession bounds the live cap runs one session may register.
const MaxCapRunsPerSession = 64

func (s *Store) lapseCutoff() string { return s.now().UTC().Add(-ReservationGrace).Format(timeFmt) }

// refuseWhileCapRuns is the reservation side of the latch: ErrAccountingMode while a live cap run is registered on the
// session. It runs inside the reserving transaction.
func (s *Store) refuseWhileCapRuns(ctx context.Context, x dbx, profileID string, sessionID int64) error {
	var n int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_session_cap_runs WHERE profile_id = ? AND session_id = ? AND lease_until >= ?`,
		profileID, sessionID, s.lapseCutoff()).Scan(&n); err != nil {
		return fmt.Errorf("tasks: reading the cap runs of session #%d: %w", sessionID, err)
	}
	if n > 0 {
		return fmt.Errorf("%w: %d run(s) of session #%d are accounted on its cap, so no reservation is taken until they end", ErrAccountingMode, n, sessionID)
	}
	return nil
}

// liveCapRunsBelowTx lists the live cap runs of the session and of every session below it, oldest first (the same
// liveness as the latch: a row lapsed past the grace counts for nothing). It runs inside the writing transaction of the
// caller, so a run cannot register between the read and the write.
func (s *Store) liveCapRunsBelowTx(ctx context.Context, x dbx, profileID string, sessionID int64) ([]CapRun, error) {
	rows, err := x.QueryContext(ctx, `WITH RECURSIVE sub(id, depth) AS (
			SELECT ?, 1
			UNION
			SELECT t.id, sub.depth + 1 FROM task_sessions t JOIN sub ON t.parent_session_id = sub.id
			WHERE t.profile_id = ? AND sub.depth < ?)
		SELECT id, session_id, owner_id, run_ref, lease_until FROM task_session_cap_runs
		WHERE profile_id = ? AND lease_until >= ? AND session_id IN (SELECT id FROM sub) ORDER BY id`,
		sessionID, profileID, s.walkLimit(ctx, sessionID), profileID, s.lapseCutoff())
	if err != nil {
		return nil, fmt.Errorf("tasks: reading the cap runs below session #%d: %w", sessionID, err)
	}
	defer rows.Close()
	var out []CapRun
	for rows.Next() {
		r := CapRun{ProfileID: profileID}
		if err := rows.Scan(&r.ID, &r.SessionID, &r.OwnerID, &r.RunRef, &r.LeaseUntil); err != nil {
			return nil, fmt.Errorf("tasks: reading the cap runs below session #%d: %w", sessionID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: reading the cap runs below session #%d: %w", sessionID, err)
	}
	return out, nil
}

// refuseLoweringWhileCapRuns is ErrAccountingMode while a live cap run is on the session or below it. A cap run holds
// no money and reads what is left of the budget as it starts, so a budget lowered under it (or set where there was
// none) is not accounted against what it may still spend. It names up to five runs.
func (s *Store) refuseLoweringWhileCapRuns(ctx context.Context, x dbx, se Session) error {
	runs, err := s.liveCapRunsBelowTx(ctx, x, se.ProfileID, se.ID)
	if err != nil || len(runs) == 0 {
		return err
	}
	var named []string
	for _, r := range runs[:min(len(runs), 5)] {
		named = append(named, fmt.Sprintf("run %q of owner %q on session #%d", r.RunRef, r.OwnerID, r.SessionID))
	}
	more := ""
	if len(runs) > len(named) {
		more = fmt.Sprintf(" and %d more", len(runs)-len(named))
	}
	return fmt.Errorf("%w: %d run(s) on session #%d or below are accounted on its cap, so its budget is not lowered while they run: %s%s. Pause the session so its runs drain, then set the budget",
		ErrAccountingMode, len(runs), se.ID, strings.Join(named, ", "), more)
}

// liveReservationsTx counts the reservations of the session that are live and not lapsed past the grace.
func (s *Store) liveReservationsTx(ctx context.Context, x dbx, profileID string, sessionID int64) (int, error) {
	var n int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_session_reservations WHERE profile_id = ? AND session_id = ? AND state = 'live' AND lease_until >= ?`,
		profileID, sessionID, s.lapseCutoff()).Scan(&n); err != nil {
		return 0, fmt.Errorf("tasks: reading the reservations of session #%d: %w", sessionID, err)
	}
	return n, nil
}

// BeginCapRun registers a run that is accounted on the session's cap. It is refused with ErrAccountingMode while a
// live reservation is on the session, and with ErrLimit at MaxCapRunsPerSession live runs. It sweeps the dead rows
// of the session first. ownerID and runRef are 1 to 128 and 1 to 256 bytes of text with no NUL.
func (s *Store) BeginCapRun(ctx context.Context, profileID string, sessionID int64, ownerID, runRef string) (CapRun, error) {
	if err := reservationText("the owner id", ownerID, maxOwnerIDBytes); err != nil {
		return CapRun{}, err
	}
	if err := reservationText("the run ref", runRef, maxWorkerRefBytes); err != nil {
		return CapRun{}, err
	}
	var out CapRun
	err := s.tx(ctx, func(x dbx) error {
		var one int
		if err := x.QueryRowContext(ctx, `SELECT 1 FROM task_sessions WHERE profile_id = ? AND id = ?`, profileID, sessionID).Scan(&one); err != nil {
			return sessionLookup(err, sessionID)
		}
		n, err := s.liveReservationsTx(ctx, x, profileID, sessionID)
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: %d reservation(s) of session #%d are live, so no run is accounted on its cap until they end", ErrAccountingMode, n, sessionID)
		}
		if _, err := x.ExecContext(ctx, `DELETE FROM task_session_cap_runs WHERE profile_id = ? AND session_id = ? AND lease_until < ?`, profileID, sessionID, s.lapseCutoff()); err != nil {
			return fmt.Errorf("tasks: sweeping the cap runs of session #%d: %w", sessionID, err)
		}
		var live int
		if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_session_cap_runs WHERE profile_id = ? AND session_id = ?`, profileID, sessionID).Scan(&live); err != nil {
			return fmt.Errorf("tasks: reading the cap runs of session #%d: %w", sessionID, err)
		}
		if live >= MaxCapRunsPerSession {
			return limit("session #%d has %d runs in flight on its cap", sessionID, live)
		}
		lease := s.now().UTC().Add(ReservationLease).Format(timeFmt)
		res, err := x.ExecContext(ctx, `INSERT INTO task_session_cap_runs (profile_id, session_id, owner_id, run_ref, lease_until, created_at) VALUES (?,?,?,?,?,?)`,
			profileID, sessionID, ownerID, runRef, lease, s.stamp())
		if err != nil {
			return fmt.Errorf("tasks: registering a run on session #%d: %w", sessionID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("tasks: registering a run on session #%d: %w", sessionID, err)
		}
		out = CapRun{ID: id, ProfileID: profileID, SessionID: sessionID, OwnerID: ownerID, RunRef: runRef, LeaseUntil: lease}
		return nil
	})
	return out, err
}

// HeartbeatCapRun renews the owner's registration. ErrReservationLost says it is gone (ended, swept, its session
// deleted, another owner's), or that it lapsed past the grace and a reservation entered the session meanwhile: the
// run must stop, it may not go on beside the hold. A lapsed row with nothing else on the session is renewed, as a
// reservation of a suspended machine is.
func (s *Store) HeartbeatCapRun(ctx context.Context, profileID string, id int64, ownerID string) error {
	return s.tx(ctx, func(x dbx) error {
		var session int64
		var lease string
		err := x.QueryRowContext(ctx, `SELECT session_id, lease_until FROM task_session_cap_runs WHERE id = ? AND owner_id = ? AND profile_id = ?`, id, ownerID, profileID).Scan(&session, &lease)
		if err != nil {
			return fmt.Errorf("%w: cap run #%d", ErrReservationLost, id)
		}
		if lease < s.lapseCutoff() {
			n, err := s.liveReservationsTx(ctx, x, profileID, session)
			if err != nil {
				return err
			}
			if n > 0 {
				return fmt.Errorf("%w: cap run #%d lapsed and the session has a reservation now", ErrReservationLost, id)
			}
		}
		res, err := x.ExecContext(ctx, `UPDATE task_session_cap_runs SET lease_until = MAX(lease_until, ?) WHERE id = ? AND owner_id = ? AND profile_id = ?`,
			s.now().UTC().Add(ReservationLease).Format(timeFmt), id, ownerID, profileID)
		if err != nil {
			return fmt.Errorf("tasks: renewing cap run #%d: %w", id, err)
		}
		return oneRow(res, id)
	})
}

// EndCapRun removes the owner's registration (the run is over). A row that is gone, or not the caller's, is not an error.
func (s *Store) EndCapRun(ctx context.Context, profileID string, id int64, ownerID string) error {
	return s.tx(ctx, func(x dbx) error {
		if _, err := x.ExecContext(ctx, `DELETE FROM task_session_cap_runs WHERE id = ? AND owner_id = ? AND profile_id = ?`, id, ownerID, profileID); err != nil {
			return fmt.Errorf("tasks: ending cap run #%d: %w", id, err)
		}
		return nil
	})
}
