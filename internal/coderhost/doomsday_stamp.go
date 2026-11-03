package coderhost

import "fmt"

// StampDoomsdayRun records which open-ended run (charter id and generation) the session's current turn belongs to, so a stop of the run
// finds the session's tokens. It writes only the session's current turn (a stale call changes nothing and is an error).
func (s *Store) StampDoomsdayRun(profileID, conversationID, turnID, charterID string, generation int64) error {
	res, err := s.db.Exec(`UPDATE coder_sessions SET doomsday_generation = ?, doomsday_charter_id = ?, updated_at = ?
		WHERE profile_id = ? AND conversation_id = ? AND turn_id = ?`, generation, charterID, s.stamp(), profileID, conversationID, turnID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("coder host: the session of %s no longer names turn %s", conversationID, turnID)
	}
	return nil
}
