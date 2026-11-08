// Package strategy is the strategy layer's store for a mission's pursuit (T6): directions, epochs, judgments and the
// novelty ledger, over the mission_strategy_* tables (migration 165), keyed by the mission's task session.
//
// It implements the epoch engine's Store and the policy intake's Ledger and RunState (the assertions are in a test file, so
// that this package does not count as an importer of the doomsday strategy libraries). The libraries still speak of a run
// by (profile, generation): here the profile is the decimal task session id and the generation is always 1, and any other
// pair is refused as invalid.
//
// NOT TAMPER-EVIDENT. Unlike the open-ended mode's store (migration 156) these rows carry no MAC and no audit line: the
// database triggers only guard the rules (rows are written while the session is running and move forward only, never
// replaced, judgments and the ledger insert-only), and a writer with the database file can write anything. Whatever the
// strategy layer recorded here is therefore a record of what the engine did, never evidence for a finish: the finish
// verifier re-runs the confirmed criteria itself and must not read this history as proof.
//
// Library only: it has no CLI verb and no MCP tool. Agent text reaches these rows only through policy.Proposer and the
// validated judge path. A guard test names the only files that may import this package (today none). While the session is
// paused nothing is written; the one move left after that is closing an unfinished epoch as abandoned.
package strategy
