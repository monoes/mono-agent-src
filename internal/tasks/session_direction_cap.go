package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// The operator's mission_directions (K): how many direction sessions an agent keeps open below one session. 0 or unset
// is the default; nothing goes past the hard cap. The coder host's settings validation uses the same two numbers.
const (
	DefaultMissionDirections = 3
	HardMissionDirections    = 6
)

// checkDirectionCap refuses an AGENT's new direction session when the parent already holds K open ones. It runs in the
// insert transaction of CreateSession, which holds the write lock, so two creators racing for the last place are
// serialized and the second sees the first. It guards every path that makes a direction below a parent (the coder
// host's session_spawn and `task session new`), because they all end in CreateSession. K is read here, in the same
// transaction, from the stored settings of the parent's chat (the lead's), so a change the operator committed counts at
// once. The operator is not held to it (as for checkChildFence): its hand-made direction is its own decision.
func (s *Store) checkDirectionCap(ctx context.Context, x dbx, profileID string, par Session, kind string, actor Actor) error {
	if actor.Kind != Agent || kind != SessionKindDirection {
		return nil
	}
	k, err := missionDirectionsOf(ctx, x, profileID, par.ConversationID)
	if err != nil {
		return err
	}
	var open int
	if err := x.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND parent_session_id = ? AND kind = ? AND `+openSessionSQL,
		profileID, par.ID, SessionKindDirection).Scan(&open); err != nil {
		return fmt.Errorf("tasks: counting the directions of session #%d: %w", par.ID, err)
	}
	if open >= k {
		return limit("session #%d already has %d open directions (the operator's mission_directions is %d)", par.ID, open, k)
	}
	return nil
}

// missionDirectionsOf is K for the chat conversationID: its stored mission_directions, the default when none is set
// (or the session has no chat), never past the hard cap. A stored document that cannot be read fails closed.
func missionDirectionsOf(ctx context.Context, x dbx, profileID, conversationID string) (int, error) {
	k := 0
	if conversationID != "" {
		var raw string
		err := x.QueryRowContext(ctx, `SELECT settings FROM coder_sessions WHERE profile_id = ? AND conversation_id = ?`, profileID, conversationID).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("tasks: reading mission_directions of %s: %w", conversationID, err)
		}
		if raw != "" {
			var doc struct {
				K int `json:"mission_directions"`
			}
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				return 0, fmt.Errorf("tasks: the stored settings of %s are not readable: %w", conversationID, err)
			}
			k = doc.K
		}
	}
	if k <= 0 {
		k = DefaultMissionDirections
	}
	return min(k, HardMissionDirections), nil
}
