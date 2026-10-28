package coderhost

import "fmt"

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

// ownedByAnother says whether a row other than this task's reservation of that generation names the conversation
// as its child or its pending conversation.
func (s *Store) ownedByAnother(profileID string, sessionID, taskID, generation int64, conv string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?)
		AND NOT (session_id = ? AND task_id = ? AND generation = ?)`, profileID, conv, conv, sessionID, taskID, generation).Scan(&n)
	return n > 0, err
}
