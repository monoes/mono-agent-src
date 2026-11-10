package storage

import (
	"database/sql"
	"strings"
	"testing"
)

// Migration 169 adds the goal check tables (a check, its machine results, the person's rubric answers) and the person's switch
// for unconfined checks. It is additive: a database at the 168 head keeps its rows and a second run changes nothing.
func TestMigration169IsAdditiveFromTheHeadBelow(t *testing.T) {
	db := open156(t)
	applyMigrationsBelow(t, db, 169)
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name) VALUES ('p', 'p')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO mission_pursuit_state (session_id, updated_at) VALUES (1, 'x')`); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if err := db.ApplyMigrations(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for _, tb := range []string{"mission_goal_checks", "mission_goal_check_results", "mission_goal_answers"} {
			var n int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s: %d rows, %v", tb, n, err)
			}
		}
		var base int
		if err := db.DB.QueryRow(`SELECT base_check_id FROM mission_pursuit_state WHERE session_id = 1`).Scan(&base); err != nil || base != 0 {
			t.Fatalf("the existing state row after the upgrade: %d %v", base, err)
		}
	}
	var v int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 169`).Scan(&v); err != nil || v != 1 {
		t.Fatalf("169 recorded %d times, %v", v, err)
	}
}

func exec169(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func refused169(t *testing.T, db *sql.DB, want, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	if err == nil {
		t.Fatalf("the database accepted: %s", q)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: refused for the wrong reason: %v", q, err)
	}
}

func count169(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// world169 is a profile with one mission session whose goal (a command, two rubric criteria) is locked and whose pursuit runs.
func world169(t *testing.T) *sql.DB {
	t.Helper()
	db := openMigrated156(t).DB
	exec169(t, db, `INSERT INTO profiles (id, name) VALUES ('p', 'p')`)
	exec169(t, db, `INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`)
	exec169(t, db, `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (1, 'proposed', 'GH', 'lead', 'x', 'x')`)
	exec169(t, db, `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, hash) VALUES (1, 1, 'command', 'go test', 'h1'), (1, 2, 'rubric', 'reads well', 'h2'), (1, 3, 'rubric', 'is kind', 'h3')`)
	exec169(t, db, `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x'`)
	exec169(t, db, `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (1, 1, 'you', 't0', 't0')`)
	exec169(t, db, `UPDATE mission_goals SET state = 'locked', locked_at = 'x'`)
	return db
}

const newCheck169 = `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, outcome, started_at, written_by) VALUES (1, 'GH', 1, 'running', '', 't1', 'host-verifier')`

// finish169 ends check 1 the way the host does, with the given outcome.
func finish169(t *testing.T, db *sql.DB, outcome string) {
	t.Helper()
	exec169(t, db, `UPDATE mission_goal_checks SET state = 'done', outcome = ?, reviewer_verdict = 'ok', ended_at = 't2' WHERE id = 1`, outcome)
}

func TestACheckIsBornRunningForALockedGoalByTheHostAlone(t *testing.T) {
	db := world169(t)
	// the goal hash must be the locked goal's
	refused169(t, db, "locked goal", `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, started_at, written_by) VALUES (1, 'OTHER', 1, 'running', 't1', 'host-verifier')`)
	// born running, with no outcome, not ended
	refused169(t, db, "locked goal", `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, outcome, started_at, ended_at, written_by) VALUES (1, 'GH', 1, 'done', 'passed-machine', 't1', 't2', 'host-verifier')`)
	refused169(t, db, "locked goal", `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, outcome, started_at, written_by) VALUES (1, 'GH', 1, 'running', 'accepted', 't1', 'host-verifier')`)
	// only the host verifier writes a check
	refused169(t, db, "", `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, started_at, written_by) VALUES (1, 'GH', 1, 'running', 't1', 'you')`)
	refused169(t, db, "", `INSERT INTO mission_goal_checks (session_id, goal_hash, round, state, started_at, written_by) VALUES (1, 'GH', 1, 'running', 't1', '')`)
	exec169(t, db, newCheck169)
	// one running check per session
	refused169(t, db, "one check", newCheck169)
	// a replace cannot swap the row
	refused169(t, db, "never replaced", `INSERT OR REPLACE INTO mission_goal_checks (id, session_id, goal_hash, round, state, started_at, written_by) VALUES (1, 1, 'GH', 1, 'running', 't9', 'host-verifier')`)
	if n := count169(t, db, `SELECT COUNT(*) FROM mission_goal_checks WHERE started_at = 't1'`); n != 1 {
		t.Fatalf("the check row was replaced or duplicated: %d", n)
	}
}

func TestACheckIsOnlyForALockedGoal(t *testing.T) {
	db := openMigrated156(t).DB
	exec169(t, db, `INSERT INTO profiles (id, name) VALUES ('p', 'p')`)
	exec169(t, db, `INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`)
	refused169(t, db, "locked goal", newCheck169) // no goal at all
	exec169(t, db, `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (1, 'proposed', 'GH', 'lead', 'x', 'x')`)
	refused169(t, db, "locked goal", newCheck169) // a proposal
	exec169(t, db, `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, hash) VALUES (1, 1, 'rubric', 'r', 'h')`)
	exec169(t, db, `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x'`)
	refused169(t, db, "locked goal", newCheck169) // confirmed, not locked
	exec169(t, db, `UPDATE mission_goals SET state = 'locked', locked_at = 'x'`)
	exec169(t, db, newCheck169)
}

func TestACheckMovesForwardOnlyAndItsOutcomeIsWrittenOnce(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	// a running check may not take the person's outcomes, or an outcome without ending
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'accepted', ended_at = 't2' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'rejected', ended_at = 't2' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = '', ended_at = 't2' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed' WHERE id = 1`) // not ended
	// what a check was started with never changes
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed', ended_at = 't2', goal_hash = 'OTHER' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed', ended_at = 't2', written_by = 'you' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed', ended_at = 't2', started_at = 't0' WHERE id = 1`)
	exec169(t, db, `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed', ended_at = 't2' WHERE id = 1`)
	// the outcome is written once: failed stays failed, whatever the next value
	for _, o := range []string{"passed-machine", "accepted", "rejected", "error", ""} {
		refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET outcome = ? WHERE id = 1`, o)
	}
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'running' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'abandoned' WHERE id = 1`)
}

func TestAnAbandonedCheckIsFinalAndHasNoOutcome(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET state = 'abandoned', outcome = 'passed-machine', ended_at = 't2' WHERE id = 1`)
	exec169(t, db, `UPDATE mission_goal_checks SET state = 'abandoned', ended_at = 't2' WHERE id = 1`)
	for _, q := range []string{
		`UPDATE mission_goal_checks SET state = 'done', outcome = 'passed-machine' WHERE id = 1`,
		`UPDATE mission_goal_checks SET state = 'running' WHERE id = 1`,
		`UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`,
	} {
		refused169(t, db, "moves forward", q)
	}
	exec169(t, db, newCheck169) // the next check may start once none runs
}

func TestAChecksDetailsAreFilledAtTheEndAndFrozenAfter(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	exec169(t, db, `UPDATE mission_goal_checks SET state = 'done', outcome = 'passed-machine', reviewer_verdict = 'ok', reviewer_pin = 'a/b', subject_digest = 'D', cost_micro = 5, cost_known = 1, ended_at = 't2' WHERE id = 1`)
	// after the end only the outcome moves (passed-machine to the person's verdicts), and nothing else
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET subject_digest = 'E' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET reviewer_verdict = 'block' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET cost_micro = 0 WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET ended_at = 't3' WHERE id = 1`)
}

func TestACheckIsNeverDeletedExceptWithItsSession(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	exec169(t, db, `INSERT INTO mission_goal_check_results (check_id, pos, criterion_hash, kind, result) VALUES (1, 1, 'h1', 'command', 'pass')`)
	refused169(t, db, "never deleted", `DELETE FROM mission_goal_checks WHERE id = 1`)
	refused169(t, db, "never deleted", `DELETE FROM mission_goal_check_results WHERE check_id = 1`)
	finish169(t, db, "passed-machine")
	exec169(t, db, `INSERT INTO mission_goal_answers (check_id, pos, criterion_hash, answer, dossier_hash, answered_by, answered_at) VALUES (1, 2, 'h2', 'yes', 'D', 'you', 't3')`)
	refused169(t, db, "never deleted", `DELETE FROM mission_goal_answers WHERE check_id = 1`)
	refused169(t, db, "never deleted", `DELETE FROM mission_goal_checks`)
	exec169(t, db, `DELETE FROM task_sessions WHERE id = 1`) // the cascade is the one removal that goes through
	for _, tb := range []string{"mission_goal_checks", "mission_goal_check_results", "mission_goal_answers"} {
		if n := count169(t, db, `SELECT COUNT(*) FROM `+tb); n != 0 {
			t.Fatalf("%s: %d rows after the session went", tb, n)
		}
	}
}

func TestResultsAreInsertOnlyWhileTheCheckRunsAndMatchTheLockedCriteria(t *testing.T) {
	db := world169(t)
	const ins = `INSERT INTO mission_goal_check_results (check_id, pos, criterion_hash, kind, result) VALUES (?, ?, ?, ?, ?)`
	refused169(t, db, "running check", ins, 1, 1, "h1", "command", "pass") // no check yet
	exec169(t, db, newCheck169)
	refused169(t, db, "rubric", ins, 1, 2, "h2", "rubric", "person")      // a rubric criterion is the person's, never a machine result
	refused169(t, db, "rubric", ins, 1, 2, "h2", "command", "pass")       // pos 2 is a rubric criterion whatever the row says
	refused169(t, db, "locked criteri", ins, 1, 1, "WRONG", "command", "pass") // the hash of the locked row
	refused169(t, db, "locked criteri", ins, 1, 9, "h1", "command", "pass")    // no such criterion
	refused169(t, db, "locked criteri", ins, 1, 1, "h1", "file", "pass")       // the kind of the locked row
	refused169(t, db, "", ins, 1, 1, "h1", "command", "weird")
	exec169(t, db, ins, 1, 1, "h1", "command", "pass")
	refused169(t, db, "never replaced", `INSERT OR REPLACE INTO mission_goal_check_results (check_id, pos, criterion_hash, kind, result) VALUES (1, 1, 'h1', 'command', 'fail')`)
	refused169(t, db, "never change", `UPDATE mission_goal_check_results SET result = 'fail' WHERE check_id = 1`)
	refused169(t, db, "", `INSERT INTO mission_goal_check_results (check_id, pos, criterion_hash, kind, result, stdout_excerpt) VALUES (1, 1, 'h1', 'command', 'pass', ?)`, strings.Repeat("x", 1025))
	if r := count169(t, db, `SELECT COUNT(*) FROM mission_goal_check_results WHERE result = 'pass'`); r != 1 {
		t.Fatalf("results: %d", r)
	}
	finish169(t, db, "failed")
	refused169(t, db, "running check", ins, 1, 1, "h1", "command", "pass") // a finished check takes no more
}

const ans169 = `INSERT INTO mission_goal_answers (check_id, pos, criterion_hash, answer, note, dossier_hash, answered_by, answered_at) VALUES (1, ?, ?, ?, ?, 'D', ?, 't3')`

func TestAnAnswerNeedsAPassedCheckAndAReviewerOk(t *testing.T) {
	db := world169(t)
	refused169(t, db, "passed", ans169, 2, "h2", "yes", "", "you") // no check
	exec169(t, db, newCheck169)
	refused169(t, db, "passed", ans169, 2, "h2", "yes", "", "you") // still running
	exec169(t, db, `UPDATE mission_goal_checks SET state = 'done', outcome = 'failed', reviewer_verdict = 'ok', ended_at = 't2' WHERE id = 1`)
	refused169(t, db, "passed", ans169, 2, "h2", "yes", "", "you") // failed

	db2 := world169(t) // a reviewer that blocked
	exec169(t, db2, newCheck169)
	exec169(t, db2, `UPDATE mission_goal_checks SET state = 'done', outcome = 'passed-machine', reviewer_verdict = 'block', ended_at = 't2' WHERE id = 1`)
	refused169(t, db2, "passed", ans169, 2, "h2", "yes", "", "you")
}

func TestAnAnswerIsForARubricCriterionWithItsHashByANamedPerson(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	finish169(t, db, "passed-machine")
	refused169(t, db, "rubric criterion", ans169, 1, "h1", "yes", "", "you")   // a command criterion is the machine's
	refused169(t, db, "rubric criterion", ans169, 2, "WRONG", "yes", "", "you") // the hash of the locked row
	refused169(t, db, "rubric criterion", ans169, 2, "h3", "yes", "", "you")    // another criterion's hash
	refused169(t, db, "rubric criterion", ans169, 9, "h2", "yes", "", "you")    // no such criterion
	refused169(t, db, "", ans169, 2, "h2", "maybe", "", "you")                  // yes or no
	refused169(t, db, "", ans169, 2, "h2", "yes", "", "")                       // a named person
	refused169(t, db, "", ans169, 2, "h2", "no", strings.Repeat("n", 501), "you")
	exec169(t, db, ans169, 2, "h2", "yes", "", "you")
	refused169(t, db, "never replaced", `INSERT OR REPLACE INTO mission_goal_answers (check_id, pos, criterion_hash, answer, dossier_hash, answered_by, answered_at) VALUES (1, 2, 'h2', 'no', 'D', 'you', 't4')`)
	refused169(t, db, "never replaced", ans169, 2, "h2", "no", "", "you") // one answer per criterion
	refused169(t, db, "never change", `UPDATE mission_goal_answers SET answer = 'no' WHERE check_id = 1`)
	if n := count169(t, db, `SELECT COUNT(*) FROM mission_goal_answers WHERE answer = 'yes'`); n != 1 {
		t.Fatalf("answers: %d", n)
	}
}

func TestACheckIsRejectedOnlyOnANoAndAcceptedOnlyOnEveryYes(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	finish169(t, db, "passed-machine")
	refused169(t, db, "answer", `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`) // nothing answered
	refused169(t, db, "answer", `UPDATE mission_goal_checks SET outcome = 'rejected' WHERE id = 1`)
	exec169(t, db, ans169, 2, "h2", "yes", "", "you")
	refused169(t, db, "answer", `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`) // c3 is not answered
	refused169(t, db, "answer", `UPDATE mission_goal_checks SET outcome = 'rejected' WHERE id = 1`) // only yes so far
	exec169(t, db, ans169, 3, "h3", "no", "unkind", "you")
	refused169(t, db, "answer", `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`) // a no
	exec169(t, db, `UPDATE mission_goal_checks SET outcome = 'rejected' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`)
	refused169(t, db, "", ans169, 3, "h3", "yes", "", "you") // a rejected check takes no more answers
}

func TestAnAcceptanceNeedsEveryYesAndTheFrozenReviewerOk(t *testing.T) {
	db := world169(t)
	exec169(t, db, newCheck169)
	finish169(t, db, "passed-machine")
	exec169(t, db, ans169, 2, "h2", "yes", "", "you")
	exec169(t, db, ans169, 3, "h3", "yes", "", "you")
	// the reviewer's verdict is frozen once the check is done, so an accept cannot ride on a changed one
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET outcome = 'accepted', reviewer_verdict = 'block' WHERE id = 1`)
	exec169(t, db, `UPDATE mission_goal_checks SET outcome = 'accepted' WHERE id = 1`)
	refused169(t, db, "moves forward", `UPDATE mission_goal_checks SET outcome = 'rejected' WHERE id = 1`)
}

func TestTheSwitchForUnconfinedChecksChangesOnlyWithAPersonNamedAndTheDateMoved(t *testing.T) {
	db := world169(t)
	if n := count169(t, db, `SELECT unconfined_checks_ok FROM mission_pursuit WHERE session_id = 1`); n != 0 {
		t.Fatalf("the switch is %d: it is born off", n)
	}
	const why = "unconfined"
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_checks_ok = 1 WHERE session_id = 1`)                      // nobody named, date unmoved
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_checks_ok = 1, unconfined_by = 'you' WHERE session_id = 1`) // date unmoved
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_checks_ok = 1, updated_at = 't1' WHERE session_id = 1`)     // nobody named
	refused169(t, db, "", `UPDATE mission_pursuit SET unconfined_checks_ok = 2, unconfined_by = 'you', updated_at = 't1' WHERE session_id = 1`)
	exec169(t, db, `UPDATE mission_pursuit SET unconfined_checks_ok = 1, unconfined_by = 'you', updated_at = 't1' WHERE session_id = 1`)
	// off again the same way
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_checks_ok = 0 WHERE session_id = 1`)
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_checks_ok = 0, unconfined_by = '', updated_at = 't2' WHERE session_id = 1`)
	// the recorded person changes only with the switch
	refused169(t, db, why, `UPDATE mission_pursuit SET unconfined_by = 'someone', updated_at = 't2' WHERE session_id = 1`)
	exec169(t, db, `UPDATE mission_pursuit SET unconfined_checks_ok = 0, unconfined_by = 'you', updated_at = 't2' WHERE session_id = 1`)
	// the other rules of a started pursuit still hold
	refused169(t, db, "limits", `UPDATE mission_pursuit SET away_budget_pct = 99 WHERE session_id = 1`)
}

func TestAPursuitIsBornWithTheSwitchOff(t *testing.T) {
	db := openMigrated156(t).DB
	exec169(t, db, `INSERT INTO profiles (id, name) VALUES ('p', 'p')`)
	exec169(t, db, `INSERT INTO task_sessions (profile_id, title, kind, created_by, created_at, updated_at) VALUES ('p', 't', 'mission', 'you', 'x', 'x')`)
	exec169(t, db, `INSERT INTO mission_goals (session_id, state, criteria_hash, proposed_by, created_at, updated_at) VALUES (1, 'proposed', 'GH', 'lead', 'x', 'x')`)
	exec169(t, db, `INSERT INTO mission_goal_criteria (session_id, pos, kind, spec, hash) VALUES (1, 1, 'rubric', 'r', 'h')`)
	exec169(t, db, `UPDATE mission_goals SET state = 'confirmed', confirmed_by = 'you', confirmed_at = 'x'`)
	refused169(t, db, "unconfined", `INSERT INTO mission_pursuit (session_id, enabled, locked_by, unconfined_checks_ok, unconfined_by, created_at, updated_at) VALUES (1, 1, 'you', 1, 'you', 'x', 'x')`)
	exec169(t, db, `INSERT INTO mission_pursuit (session_id, enabled, locked_by, created_at, updated_at) VALUES (1, 1, 'you', 'x', 'x')`)
}
