package coderhost

import "fmt"

// The decisions ledger rows of session_spawn (sessionspawn.go). This file is part of the ledger code: only it and the
// parked-question code write the ledger (autonomy_guard_test.go).

// recordSpawn writes the decision to the ledger of the spawner's conversation: the reason asked for, the scope read from
// the tasks, and what was decided. A refusal is written as the model's row (it is the agent's request that was
// turned down, and a model's rows rotate only among its own, so a loop of refusals cannot push the audit trail of
// accepted spawns out); an acceptance as the master's. Class scope_growth is the ledger's nearest class: a session
// is more scope.
func (s *Store) recordSpawn(c Caller, title string, req SessionSpawnRequest, f SpawnFacts, decision, why string) error {
	_, err := s.Record(spawnEntry(c, title, req, f, decision, why))
	return err
}

func spawnEntry(c Caller, title string, req SessionSpawnRequest, f SpawnFacts, decision, why string) LedgerEntry {
	by := WhoMaster
	if decision == AdviceRefuse {
		by = WhoModel
	}
	return LedgerEntry{
		ProfileID: c.ProfileID, ConversationID: c.ConversationID, Class: "scope_growth", MadeBy: by,
		Question:     fmt.Sprintf("spawn a %s session %q?", req.Kind, title),
		Choice:       decision,
		Alternatives: []string{"run the work in this session"},
		Reason: fmt.Sprintf("reason=%s; scope: %d open tasks (%d above size s), %d writers, parallel=%t, high risk=%t; %s",
			req.Reason, f.OpenTasks, f.Larger, f.Writers, f.Parallel, f.HighRisk, why),
	}
}

// operatorKeepEntry is the acceptance row of a held spawn the operator keeps (`spawn-intents resolve --keep`). The
// table allows only model and master rows, so it is the master's kind of row (an acceptance); the reason says the
// operator wrote it.
func operatorKeepEntry(profile, spawner, child string, gen int64) LedgerEntry {
	return LedgerEntry{
		ProfileID: profile, ConversationID: spawner, Class: "scope_growth", MadeBy: WhoMaster,
		Question:     fmt.Sprintf("keep the held spawn %q?", child),
		Choice:       AdviceAccept,
		Alternatives: []string{"discard the held spawn"},
		Reason:       fmt.Sprintf("resolved by the operator with `coder sessions spawn-intents resolve --keep` (intent generation %d); the child and its task session were left as they are", gen),
	}
}
