package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// Carved is the budget the children of a session hold; Remaining is what the
// session can still hand out (-1 when it has no cap).
func (s *Store) Carved(profileID, conversationID string) (carved float64, err error) {
	return carvedIn(s.db, profileID, conversationID)
}

// carvedIn is Carved read through q (a snapshot, when it must agree with the rows read beside it).
func carvedIn(q querier, profileID, conversationID string) (carved float64, err error) {
	err = q.QueryRow(`SELECT COALESCE(SUM(budget_usd), 0) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ?`, profileID, conversationID).Scan(&carved)
	return carved, err
}

// Budget returns a session's cap (0 = none).
func (s *Store) Budget(profileID, conversationID string) (float64, error) {
	return budgetIn(s.db, profileID, conversationID)
}

// budgetIn is Budget read through q.
func budgetIn(q querier, profileID, conversationID string) (float64, error) {
	var b float64
	err := q.QueryRow(`SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&b)
	return b, err
}

// SetBudget sets a session's cap (the operator's; 0 = none). A cap cannot be set
// below what the session already handed to its children.
func (s *Store) SetBudget(profileID, conversationID string, usd float64) error {
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return fmt.Errorf("%w: the budget must be a number of at least 0", ErrInvalid)
	}
	if _, err := s.EnsureSession(profileID, conversationID); err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE coder_sessions SET budget_usd = ?, updated_at = ? WHERE profile_id = ? AND conversation_id = ?
		AND (? = 0 OR ? + 1e-9 >= (SELECT COALESCE(SUM(k.budget_usd), 0) FROM coder_sessions k WHERE k.profile_id = ? AND k.parent_conversation_id = ?))`,
		usd, s.stamp(), profileID, conversationID, usd, usd, profileID, conversationID)
	return s.applied(res, err, "the budget is below what this session already gave its children")
}

// carveBudget gives the child its budget out of the parent's, in one statement so
// two spawns cannot both take the last of it. A parent without a cap hands out any
// amount (0 = none); a parent with one hands out a positive amount that fits.
func (s *Store) carveBudget(profileID, parent, child string, usd float64) error {
	return s.carveBudgetAdmit(profileID, parent, child, usd, nil)
}

// carveBudgetAdmit is carveBudget with the admission of hk.admit in the same transaction: asked before the statement
// and again after it, once the carve is in the rows every commitment reads. A reason is errCarveNoRoom with it.
func (s *Store) carveBudgetAdmit(profileID, parent, child string, usd float64, admit func(q querier, usd float64, written bool) (string, error)) error {
	spent, err := s.Spent(profileID, parent) // read first, and part of the statement's guard: what the master has spent is not its to give
	if err != nil {
		return err
	}
	// BEGIN IMMEDIATE (as UpdateSettings): the write lock is taken before the admission's first read, so a writer that
	// commits in between cannot make the carve's write fail at once with SQLITE_BUSY_SNAPSHOT, which busy_timeout does
	// not wait for. The carve then waits for the lock like a statement that was first always did.
	stamp := s.stamp() // read before the write lock is taken
	return dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error {
		tx := connQuerier{conn}
		if admit != nil && usd > 0 {
			if why, err := admit(tx, usd, false); err != nil {
				return err
			} else if why != "" {
				return fmt.Errorf("%w: %s", errCarveNoRoom, why)
			}
		}
		res, err := tx.Exec(`UPDATE coder_sessions SET budget_usd = ?, updated_at = ?
			WHERE profile_id = ? AND conversation_id = ? AND parent_conversation_id = ?
			AND ( (SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?) <= 0
			   OR ( ? > 0 AND ? <= (SELECT budget_usd FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?)
			        - ? - COALESCE((SELECT SUM(budget_usd) FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ? AND conversation_id != ?), 0) + 1e-9 ) )`,
			usd, stamp, profileID, child, parent, profileID, parent, usd, usd, profileID, parent, spent, profileID, parent, child)
		if err != nil {
			return err
		}
		// Told apart inside the transaction that ran the statement: no room, or the child no longer this parent's.
		err = carveVerdict(res, func() (bool, error) {
			var n int
			err := tx.QueryRow(`SELECT COUNT(*) FROM coder_sessions WHERE profile_id = ? AND conversation_id = ? AND parent_conversation_id = ?`, profileID, child, parent).Scan(&n)
			return n > 0, err
		})
		if err != nil {
			return err
		}
		if admit != nil && usd > 0 {
			if why, err := admit(tx, usd, true); err != nil {
				return err
			} else if why != "" {
				return fmt.Errorf("%w: %s", errCarveNoRoom, why)
			}
		}
		return nil
	})
}

// writeTx runs fn in a BEGIN IMMEDIATE transaction (dbconn.WithWriteTx) over a querier, for the mission-child files
// that may not import dbconn themselves.
func (s *Store) writeTx(fn func(tx querier) error) error {
	return dbconn.WithWriteTx(context.Background(), s.db, func(conn *sql.Conn) error { return fn(connQuerier{conn}) })
}

// connQuerier lends the connection that holds an open transaction to the code that takes a querier.
type connQuerier struct{ c *sql.Conn }

func (q connQuerier) Exec(query string, args ...any) (sql.Result, error) {
	return q.c.ExecContext(context.Background(), query, args...)
}
func (q connQuerier) Query(query string, args ...any) (*sql.Rows, error) {
	return q.c.QueryContext(context.Background(), query, args...)
}
func (q connQuerier) QueryRow(query string, args ...any) *sql.Row {
	return q.c.QueryRowContext(context.Background(), query, args...)
}

// carveVerdict reads what the carve statement did: nil when it carved, errCarveNoRoom when it matched nothing
// though the child is still the parent's (the share does not fit), errReservationLost when the child is gone or
// no longer the parent's, and the storage's own error when RowsAffected or the re-read failed.
func carveVerdict(res sql.Result, stillChild func() (bool, error)) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	ok, err := stillChild()
	if err != nil {
		return err
	}
	if !ok {
		return errReservationLost
	}
	return errCarveNoRoom
}

// errReservationLost is a carve whose child session vanished (deleted, or no longer the parent's) before the
// carve: not a refusal for lack of budget, so it is neither a full limit nor a wait.
var errReservationLost = errors.New("the child's session is gone or no longer the master's")

// errCarveNoRoom is the one answer of carveBudget that is a refusal: the conditional statement matched nothing
// because the share does not fit. Any other error is the storage's and is not a limit.
var errCarveNoRoom = errors.New("the carve does not fit in the master's budget")
