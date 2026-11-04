package slots

import (
	"context"
	"database/sql"
	"errors"
)

// Reconcile releases the live slots whose member is not live in the registry (the orphans of a crash) and reports how
// many slots it released. A live member's slot is never released, and when the registry cannot be read nothing is.
//
// The registry is read without holding the write lock, so only slots that already existed when the read began are
// judged (by slot id): a slot reserved afterwards is left alone. The contract for callers is that a member is
// registered before or together with its Reserve; a slot reserved for a member that registers only after a
// concurrent Reconcile read the registry could be released by it.
func (s *Store) Reconcile(ctx context.Context, profile string, generation int64) (int, error) {
	if s.view == nil {
		return 0, errors.New("governor slots: reconcile needs a registry view")
	}
	var upTo sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT max(slot_id) FROM doomsday_slots WHERE profile = ? AND generation = ?`, profile, generation).Scan(&upTo); err != nil {
		return 0, err
	}
	live, err := s.view.LiveMemberIDs(ctx, profile, generation)
	if err != nil {
		return 0, err
	}
	if !upTo.Valid {
		return 0, nil
	}
	freed := 0
	err = s.tx(ctx, func(c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, `SELECT slot_id, member_id FROM doomsday_slots WHERE profile = ? AND generation = ? AND slot_id <= ? AND released_at IS NULL`,
			profile, generation, upTo.Int64)
		if err != nil {
			return err
		}
		var orphans []any
		for rows.Next() {
			var id int64
			var member string
			if err := rows.Scan(&id, &member); err != nil {
				rows.Close()
				return err
			}
			if !live[member] {
				orphans = append(orphans, id)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, id := range orphans {
			res, err := c.ExecContext(ctx, `UPDATE doomsday_slots SET released_at = ? WHERE slot_id = ? AND released_at IS NULL`, s.now().Unix(), id)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			freed += int(n)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return freed, nil
}
