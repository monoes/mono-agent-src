package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// MissionPlannerName is the name the in-process mission planner of a chat works under. Sessions it
// started before the kind column held mission sessions carry it in created_by (kind "board"), so the
// name is reserved: only the planner itself (Actor.MissionPlanner, set by trusted code) may use it.
const MissionPlannerName = "mission-lead"

// checkMissionKind refuses an agent's attempt to start a session that would pass for the chat's
// mission: kind mission, or the planner's name. The operator and the planner may. Without this a
// prompt-injected agent could link a session to its own chat and confirm a mission with no click.
func checkMissionKind(actor Actor, kind string) error {
	if actor.Kind != Agent || actor.MissionPlanner {
		return nil
	}
	if kind == SessionKindMission {
		return operatorOnly("start a session of kind mission: the operator's mission starts it")
	}
	if actor.Name == MissionPlannerName {
		return operatorOnly("use the name " + MissionPlannerName + ": it is the mission planner's")
	}
	return nil
}

// IsMissionSession says whether a session is a chat's mission session: kind mission, or a session of
// the old kind "board" that the mission planner started (created_by is the planner's name, which
// CreateSession now reserves, so a later agent cannot forge it).
func IsMissionSession(se Session) bool {
	return se.Kind == SessionKindMission || (se.Kind == SessionKindBoard && se.CreatedBy == MissionPlannerName)
}

// dedupeFits says whether an agent's client id may answer with the session it names: that session is
// in the agent's own subtree (when it works in a session) and was started as this call asks, below the
// same parent and of the same kind. Anything else is a key another caller used. The mission planner is
// not held to the kind: its old sessions are of kind board.
func (s *Store) dedupeFits(ctx context.Context, x dbx, found Session, in SessionInput, kind string, actor Actor) bool {
	if found.ParentSessionID != in.ParentSessionID || (found.Kind != kind && !actor.MissionPlanner) {
		return false
	}
	if actor.Session == 0 {
		return true
	}
	chain, err := s.sessionChain(ctx, x, found.ID)
	return err == nil && slices.Contains(chain, actor.Session)
}

// OpenMissionSessionIn is the id of the chat's open mission session (draft, running or paused, not archived;
// IsMissionSession says which sessions are missions), 0 when it has none, read through x. A caller that holds
// the write lock on x's connection (an operator's change of the Mission toggle) can rely on the answer until
// it commits: no session of that chat can be added or reopened in between. It reads the columns directly, so a
// row ListSessionsChecked would skip as unreadable still counts.
func OpenMissionSessionIn(ctx context.Context, x Queryer, profileID, conversationID string) (int64, error) {
	var id int64
	err := x.QueryRowContext(ctx, `SELECT id FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = ''
		AND state IN (?, ?, ?) AND (kind = ? OR (kind = ? AND created_by = ?)) ORDER BY id DESC LIMIT 1`,
		profileID, conversationID, string(SessionDraft), string(SessionRunning), string(SessionPaused),
		SessionKindMission, SessionKindBoard, MissionPlannerName).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tasks: looking for the open mission session of %s: %w", conversationID, err)
	}
	return id, nil
}
