package doomsday

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/ops"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The boss of a run is a recorded identity, not whoever calls a boss tool: the top-level lead mission the operator named
// at `start --boss-session`, stored on the session row (doomsday_charter_id, doomsday_generation) once per charter. Another
// lead mission of the same profile is not it, and a run with no recorded boss has no boss tools.

// BossSource is ops.Bosses over the profile's database.
type BossSource struct {
	Wiring Wiring
}

// BossOf is the lowest-id open top-level lead mission stamped with the run's charter and generation.
func (b BossSource) BossOf(ctx context.Context, act ops.Active) (int64, bool, error) {
	if b.Wiring.DB == nil {
		return 0, false, errors.New("no database to read the run's boss from")
	}
	var id int64
	err := b.Wiring.DB.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE profile_id = ? AND doomsday_charter_id = ? AND doomsday_generation = ?
		AND COALESCE(parent_session_id, 0) = 0 AND archived_at = '' AND `+coderhost.LeadSessionSQL+` ORDER BY id LIMIT 1`,
		act.Profile, act.ID, act.Generation).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return id, true, nil
}

// designateBoss stamps the named session as the boss of the entered run. It writes once: a charter that already has a boss
// is refused, and so is a session that is not an open top-level mission of the profile (a task, direction or research session is
// one an agent can make, so it is never the boss).
func (w Wiring) designateBoss(ctx context.Context, session int64, entry Entry) error {
	if w.DB == nil {
		return errors.New("no database to record the boss in")
	}
	res, err := w.DB.ExecContext(ctx, `UPDATE task_sessions SET doomsday_charter_id = ?, doomsday_generation = ?
		WHERE profile_id = ? AND id = ? AND COALESCE(parent_session_id, 0) = 0 AND archived_at = '' AND doomsday_charter_id = '' AND kind = 'mission' AND `+coderhost.LeadSessionSQL+`
		AND NOT EXISTS (SELECT 1 FROM task_sessions o WHERE o.profile_id = ? AND o.doomsday_charter_id = ?)`,
		entry.CharterID, entry.Generation, w.Profile, session, w.Profile, entry.CharterID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("session %d is not an open top-level mission session of the profile, or the run already has a boss", session)
	}
	return nil
}

// RootOf walks the parent chain of a session to its top-level session (a cycle or a tree deeper than the charter maximum
// is refused).
func (b BossSource) RootOf(ctx context.Context, profile string, session int64) (int64, error) {
	if b.Wiring.DB == nil {
		return 0, errors.New("no database to read the session tree from")
	}
	cur := session
	for i := 0; i <= tasks.MaxCharterSessionDepth+1; i++ {
		var parent int64
		if err := b.Wiring.DB.QueryRowContext(ctx, `SELECT COALESCE(parent_session_id, 0) FROM task_sessions WHERE profile_id = ? AND id = ?`, profile, cur).Scan(&parent); err != nil {
			return 0, err
		}
		if parent == 0 {
			return cur, nil
		}
		cur = parent
	}
	return 0, errors.New("the session tree is deeper than a charter allows")
}

// AuditCounter is ops.ParkCounter and the denied-attempt count over the profile's database: the audit stream is the memory
// of what was already recorded, across the processes that build an enforcer per call.
type AuditCounter struct{ DB *sql.DB }

// ParkedCount is the number of parked decisions the generation's stream holds.
func (c AuditCounter) ParkedCount(ctx context.Context, profile string, generation int64) (int64, error) {
	return c.count(ctx, profile, generation, `kind LIKE 'ops.%.parked'`)
}

func (c AuditCounter) count(ctx context.Context, profile string, generation int64, where string) (int64, error) {
	if c.DB == nil {
		return 0, errors.New("no database to count audit rows in")
	}
	var n int64
	err := c.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND `+where, profile, generation).Scan(&n)
	return n, err
}
