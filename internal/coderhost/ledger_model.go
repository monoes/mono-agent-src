package coderhost

import "fmt"

// The decisions ledger row of a boss's model change (doomsday_model.go). This file is part of the ledger code: only it and the
// parked-question code write the ledger (autonomy_guard_test.go). The row is the master's kind (an acceptance), written to the
// ledger of the session whose model changed; class scope_growth is the ledger's nearest class, as for a spawn.
func (s *Store) recordModelChange(profileID string, boss int64, ch BossModelChange, reason string) error {
	_, err := s.Record(LedgerEntry{ProfileID: profileID, ConversationID: ch.Conversation, Class: "scope_growth", MadeBy: WhoMaster, Question: modelChangeQuestion,
		Choice: ch.Runtime + "/" + ch.To, Alternatives: []string{"keep " + ch.Runtime + "/" + ch.From},
		Reason: fmt.Sprintf("changed by the boss of the run (session %d): %s", boss, reason)})
	return err
}
