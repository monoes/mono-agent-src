package strategy

import (
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// The rules are in the database, so that no code path of any process gets around them: these tests use raw SQL.
func (f fixture) seed(t *testing.T) {
	t.Helper()
	f.openEpoch(t, 1)
	f.addDirection(t, "d1", "first direction")
	mustExec(t, f.db, `INSERT INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, model_pin, state) VALUES (?, 2, 'timer', 5, 'm', 'open')`, f.session)
	mustExec(t, f.db, `INSERT INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 2, '{}', x'01')`, f.session)
	mustExec(t, f.db, `UPDATE mission_strategy_epochs SET state = 'judged', judgment_hash = x'01' WHERE session_id = ? AND epoch_seq = 2`, f.session)
}

func TestNoInsertWhenTheSessionIsNotRunningAtDatabase(t *testing.T) {
	for _, state := range []string{"paused", "stopped", "done", "failed", "draft"} {
		f := newFixture(t)
		f.seed(t)
		f.setState(t, state)
		s := f.session
		mustRefuse(t, f.db, state+": a direction", `INSERT INTO mission_strategy_directions (session_id, direction_id, key_hash, title, state) VALUES (?, 'x', x'09', 't', 'proposed')`, s)
		mustRefuse(t, f.db, state+": an epoch", `INSERT INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, state) VALUES (?, 9, 'timer', 1, 'open')`, s)
		mustRefuse(t, f.db, state+": a judgment", `INSERT INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 1, '{}', x'02')`, s)
		mustRefuse(t, f.db, state+": a novelty row", `INSERT INTO mission_strategy_novelty (session_id, key_hash, outcome) VALUES (?, x'09', 'accepted')`, s)
		// a direction cannot move, an epoch cannot be applied or judged
		mustRefuse(t, f.db, state+": a direction move", `UPDATE mission_strategy_directions SET state = 'active' WHERE session_id = ?`, s)
		mustRefuse(t, f.db, state+": applying an epoch", `UPDATE mission_strategy_epochs SET state = 'applied' WHERE session_id = ? AND epoch_seq = 2`, s)
		mustRefuse(t, f.db, state+": judging an epoch", `UPDATE mission_strategy_epochs SET state = 'bad-judgment' WHERE session_id = ? AND epoch_seq = 1`, s)
		// the one move left
		mustExec(t, f.db, `UPDATE mission_strategy_epochs SET state = 'abandoned' WHERE session_id = ? AND epoch_seq = 1`, s)
		mustExec(t, f.db, `UPDATE mission_strategy_epochs SET state = 'abandoned' WHERE session_id = ? AND epoch_seq = 2`, s)
	}
	// and a row for a session that does not exist is refused too (the foreign key and the rule both)
	f := newFixture(t)
	mustRefuse(t, f.db, "a missing session", `INSERT INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, state) VALUES (424242, 1, 'timer', 1, 'open')`)
}

func TestNeverReplacesAtDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	s := f.session
	mustRefuse(t, f.db, "replace a direction by id", `INSERT OR REPLACE INTO mission_strategy_directions (session_id, direction_id, key_hash, title, state) VALUES (?, 'd1', x'0a', 'other', 'proposed')`, s)
	mustRefuse(t, f.db, "replace a direction by key", `INSERT OR REPLACE INTO mission_strategy_directions (session_id, direction_id, key_hash, title, state) VALUES (?, 'd9', ?, 'other', 'proposed')`, s, types.KeyHash("first direction")[:])
	mustRefuse(t, f.db, "replace an epoch", `INSERT OR REPLACE INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, state) VALUES (?, 2, 'timer', 1, 'open')`, s)
	mustRefuse(t, f.db, "upsert an epoch", `INSERT INTO mission_strategy_epochs (session_id, epoch_seq, trigger, started_at, state) VALUES (?, 1, 'timer', 1, 'open') ON CONFLICT DO UPDATE SET state = 'open'`, s)
	mustRefuse(t, f.db, "replace a judgment", `INSERT OR REPLACE INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 2, '{"x":1}', x'03')`, s)
	mustRefuse(t, f.db, "replace a novelty row", `INSERT OR REPLACE INTO mission_strategy_novelty (novelty_id, session_id, key_hash, outcome) VALUES (1, ?, x'0b', 'accepted')`, s)
	if st := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_epochs WHERE state = 'judged' AND epoch_seq = 2`); st != 1 {
		t.Error("the judged epoch was walked back")
	}
}

func TestJudgmentsAndNoveltyAreInsertOnlyAtDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	s := f.session
	mustRefuse(t, f.db, "edit a judgment", `UPDATE mission_strategy_judgments SET body = 'x' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "remove a judgment", `DELETE FROM mission_strategy_judgments WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "edit a novelty row", `UPDATE mission_strategy_novelty SET outcome = 'refused-ledger' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "remove a novelty row", `DELETE FROM mission_strategy_novelty WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "remove a direction", `DELETE FROM mission_strategy_directions WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "remove an epoch", `DELETE FROM mission_strategy_epochs WHERE session_id = ?`, s)
}

func TestJudgmentNeedsAnOpenEpochAtDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	s := f.session
	mustRefuse(t, f.db, "a judgment with no epoch", `INSERT INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 7, '{}', x'04')`, s)
	mustRefuse(t, f.db, "a second judgment for a judged epoch", `INSERT INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 2, '{}', x'05')`, s)
	mustExec(t, f.db, `UPDATE mission_strategy_epochs SET state = 'abandoned' WHERE session_id = ? AND epoch_seq = 1`, s)
	mustRefuse(t, f.db, "a judgment for an abandoned epoch", `INSERT INTO mission_strategy_judgments (session_id, epoch_seq, body, body_hash) VALUES (?, 1, '{}', x'06')`, s)
}

func TestStatesMoveForwardOnlyAtDatabase(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	s := f.session
	// epochs
	mustRefuse(t, f.db, "judged back to open", `UPDATE mission_strategy_epochs SET state = 'open' WHERE session_id = ? AND epoch_seq = 2`, s)
	mustRefuse(t, f.db, "open straight to applied", `UPDATE mission_strategy_epochs SET state = 'applied' WHERE session_id = ? AND epoch_seq = 1`, s)
	mustRefuse(t, f.db, "a judgment hash moved", `UPDATE mission_strategy_epochs SET judgment_hash = x'ff' WHERE session_id = ? AND epoch_seq = 2`, s)
	mustRefuse(t, f.db, "the trigger changed", `UPDATE mission_strategy_epochs SET trigger = 'new-evidence' WHERE session_id = ? AND epoch_seq = 1`, s)
	mustRefuse(t, f.db, "the start time changed", `UPDATE mission_strategy_epochs SET started_at = 1 WHERE session_id = ? AND epoch_seq = 1`, s)
	mustRefuse(t, f.db, "the model pin changed", `UPDATE mission_strategy_epochs SET model_pin = 'x' WHERE session_id = ? AND epoch_seq = 1`, s)
	mustExec(t, f.db, `UPDATE mission_strategy_epochs SET state = 'applied' WHERE session_id = ? AND epoch_seq = 2`, s)
	mustRefuse(t, f.db, "applied to abandoned", `UPDATE mission_strategy_epochs SET state = 'abandoned' WHERE session_id = ? AND epoch_seq = 2`, s)
	// directions
	mustRefuse(t, f.db, "proposed to finished", `UPDATE mission_strategy_directions SET state = 'finished' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "the title changed", `UPDATE mission_strategy_directions SET title = 'x' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "the key changed", `UPDATE mission_strategy_directions SET key_hash = x'aa' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "the proposer changed", `UPDATE mission_strategy_directions SET proposer_session = 'x' WHERE session_id = ?`, s)
	mustExec(t, f.db, `UPDATE mission_strategy_directions SET state = 'active', lead_session = 'lead-a' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "the lead rebound", `UPDATE mission_strategy_directions SET lead_session = 'lead-b' WHERE session_id = ?`, s)
	mustExec(t, f.db, `UPDATE mission_strategy_directions SET state = 'finished' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "finished back to active", `UPDATE mission_strategy_directions SET state = 'active' WHERE session_id = ?`, s)
	mustRefuse(t, f.db, "an unknown state", `UPDATE mission_strategy_directions SET state = 'done' WHERE session_id = ?`, s)
}

func TestSessionRemovalCascadesEvenWithRowsAndStopped(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	mustExec(t, f.db, `INSERT INTO mission_strategy_novelty (session_id, key_hash, outcome) VALUES (?, x'09', 'refused-ledger')`, f.session)
	f.setState(t, "stopped")
	mustExec(t, f.db, `DELETE FROM task_sessions WHERE id = ?`, f.session)
	for _, tb := range []string{"directions", "epochs", "judgments", "novelty"} {
		if n := count(t, f.db, `SELECT COUNT(*) FROM mission_strategy_`+tb); n != 0 {
			t.Errorf("mission_strategy_%s keeps %d rows of a removed session", tb, n)
		}
	}
}
