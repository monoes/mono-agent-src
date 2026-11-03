package coderhost

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/doomsday/ops"
)

// treeBosses is ops.Bosses over a coder store's database for these tests: the boss is the lowest-id top-level session
// (what `start --boss-session` records in production), and the root of a session is found by walking its parents.
type treeBosses struct{ db *sql.DB }

func (b treeBosses) BossOf(ctx context.Context, act ops.Active) (int64, bool, error) {
	var id int64
	if err := b.db.QueryRowContext(ctx, `SELECT COALESCE(MIN(id), 0) FROM task_sessions WHERE profile_id = ? AND COALESCE(parent_session_id, 0) = 0`, act.Profile).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, id != 0, nil
}

func (b treeBosses) RootOf(ctx context.Context, profile string, session int64) (int64, error) {
	for i := 0; i < 20; i++ {
		var parent int64
		if err := b.db.QueryRowContext(ctx, `SELECT COALESCE(parent_session_id, 0) FROM task_sessions WHERE profile_id = ? AND id = ?`, profile, session).Scan(&parent); err != nil {
			return 0, err
		}
		if parent == 0 {
			return session, nil
		}
		session = parent
	}
	return 0, sql.ErrNoRows
}
