package coderhost

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/ai"
)

// Two facts abandonSpawn needs that the reservation row alone does not carry.
//
// Whose conversation it is: the store API (SetSpawned, NotePending) can hand a conversation that an older attempt
// created to a newer one, so the older cleanup deletes it only when no row but its own reservation names it.
//
// Why the reservation ended: a refused carve is not an attempt. When deleting the conversation fails the row keeps
// the conversation for the host's stale pass, which ends it as expired; the refusal is therefore written down, as
// an event of the existing audit table (no migration), keyed to the generation and the conversation, and the stale
// pass reads it to give the attempt back.

// carveRefusedNote is the audit note that marks a reservation's refused carve.
func carveRefusedNote(generation int64, conv string) string {
	return fmt.Sprintf("carve refused: gen=%d conv=%s", generation, conv)
}

// recordCarveRefused writes the marker.
func (s *Store) recordCarveRefused(profileID string, sessionID, taskID int64, lead string, generation int64, conv string) error {
	return addMissionEvent(s.db, profileID, sessionID, taskID, "spawn", "lead:"+lead, carveRefusedNote(generation, conv), s.stamp())
}

// deleteIfNotOwnedByAnother deletes the conversation unless a row other than this task's reservation of that
// generation names it as its child or its pending conversation. The check runs inside the delete's own
// BEGIN IMMEDIATE transaction, so no reservation can take the conversation between the check and the delete;
// a conversation another row owns is left alone (ai.ErrConversationGuarded).
func (s *Store) deleteIfNotOwnedByAnother(profileID string, sessionID, taskID, generation int64, conv string) error {
	return s.deleteIfNotOwnedByAnotherAnd(profileID, sessionID, taskID, generation, conv, nil)
}

// deleteIfNotOwnedByAnotherAnd also asks extra (when set), in the same transaction, whether the delete may go on.
func (s *Store) deleteIfNotOwnedByAnotherAnd(profileID string, sessionID, taskID, generation int64, conv string, extra func(context.Context, *sql.Conn) (bool, error)) error {
	store, err := ai.NewAIStore(s.db)
	if err != nil {
		return err
	}
	return store.DeleteConversationIfUnowned(conv, profileID, func(ctx context.Context, c *sql.Conn) (bool, error) {
		var n int
		err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?)
			AND NOT (session_id = ? AND task_id = ? AND generation = ?)`, profileID, conv, conv, sessionID, taskID, generation).Scan(&n)
		if err != nil || n != 0 {
			return false, err
		}
		if extra == nil {
			return true, nil
		}
		return extra(ctx, c)
	})
}
