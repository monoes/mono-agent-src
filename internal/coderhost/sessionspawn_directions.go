package coderhost

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The operator's mission_directions (K): 0 means the default, and nothing goes past the hard cap.
const (
	defaultMissionDirections = 3
	hardMissionDirections    = 6
)

// holdDirections refuses a direction session past the operator's mission_directions for its lead. It runs right
// after the task session is inserted and counts the open directions under the lead's session, the new one included, so
// two spawns racing for the last place cannot both keep it (both may be refused, never both kept).
func (s *Store) holdDirections(ctx context.Context, c Caller, leadSession int64, made tasks.Session) error {
	if made.Kind != tasks.SessionKindDirection {
		return nil
	}
	ps, err := s.GetSettings(c.ProfileID, c.ConversationID)
	if err != nil {
		return err
	}
	k := ps.MissionDirections
	if k <= 0 {
		k = defaultMissionDirections
	}
	k = min(k, hardMissionDirections)
	var open int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND parent_session_id = ? AND kind = ? AND state IN ('draft', 'running', 'paused')`,
		c.ProfileID, leadSession, tasks.SessionKindDirection).Scan(&open); err != nil {
		return err
	}
	if open > k {
		return fmt.Errorf("%w: %d directions are already open below your session (mission_directions is %d)", ErrLimit, open-1, k)
	}
	return nil
}
