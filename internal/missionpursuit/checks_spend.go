package missionpursuit

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// What goal checks cost and how often they may run (S7c1b). The host's check step, the gate's 20% rule and `pursuit status`
// all read the same functions, so their figures cannot differ. A check row counts from the moment it starts: a running check
// is charged at CheckCapMicro, a finished one at its recorded cost when that is known, and at the cap when it is not (an
// abandoned check, a sweep at a restart, a reviewer call whose cost the runtime did not report). Never SUM(cost_micro) alone.
const (
	// MaxChecksPerAwayWindow: checks of one mission since the person last acted (rows after base_check_id, every state counts).
	MaxChecksPerAwayWindow = 2
	// MaxChecksPerMissionLifetime: checks of one mission since its latest accepted or rejected check (a person's answer starts it again).
	MaxChecksPerMissionLifetime = 6
	// MaxHostChecksPerHour: checks across ALL missions of this host that started in any hour.
	MaxHostChecksPerHour = 4
	// MaxBadReviewerRuns: this many checks in a row ended blocked because the reviewer could not be used (unavailable, invalid
	// answer, another model than the pin) stop the checks of the mission until the person acts.
	MaxBadReviewerRuns = 3
	// CheckTotalTimeout is the run time limit of one check (the library's own limit is 20 minutes).
	CheckTotalTimeout = 10 * time.Minute
)

// CheckSpend is what the checks of a mission after a ledger id cost, and how many there were.
type CheckSpend struct {
	Micro int64
	Count int
	// Parks is how many of them ended without the goal being met (failed or error: the criteria, not the checker or the machine in
	// the way): they count as parked questions in the away limits, like a question the lead parked.
	Parks int
}

// CheckSpendAfter is the spend (micro-USD) and the number of the mission's checks with an id above baseID.
func CheckSpendAfter(ctx context.Context, db *sql.DB, session, baseID int64) (CheckSpend, error) {
	var out CheckSpend
	err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN state = 'running' THEN ?1 WHEN cost_known = 1 THEN cost_micro ELSE ?1 END), 0), COUNT(*),
		COALESCE(SUM(CASE WHEN state = 'done' AND outcome IN ('failed', 'error') THEN 1 ELSE 0 END), 0)
		FROM mission_goal_checks WHERE session_id = ?2 AND id > ?3`, CheckCapMicro, session, baseID).Scan(&out.Micro, &out.Count, &out.Parks)
	if err != nil {
		return CheckSpend{}, fmt.Errorf("missionpursuit: reading the check spend: %w", err)
	}
	return out, nil
}

// CheckHead is the highest check id of the mission (0 when it has none): the person's action is measured from here.
func CheckHead(ctx context.Context, db *sql.DB, session int64) (int64, error) {
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM mission_goal_checks WHERE session_id = ?`, session).Scan(&id); err != nil {
		return 0, fmt.Errorf("missionpursuit: reading the check ledger: %w", err)
	}
	return id, nil
}

// NextCheckRound is the round the mission's next check takes: one more than every round it has had (abandoned ones included),
// read from the rows, never from a counter in memory, so it survives a restart of the host.
func NextCheckRound(ctx context.Context, db *sql.DB, session int64) (int64, error) {
	var last int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(round), 0) FROM mission_goal_checks WHERE session_id = ?`, session).Scan(&last); err != nil {
		return 0, fmt.Errorf("missionpursuit: reading the check rounds: %w", err)
	}
	return last + 1, nil
}

// HostChecksSince counts the checks of every mission that started at or after since; a start time that does not read counts.
func HostChecksSince(ctx context.Context, db *sql.DB, since time.Time) (int, error) {
	rows, err := db.QueryContext(ctx, `SELECT started_at FROM mission_goal_checks ORDER BY id DESC LIMIT 1000`)
	if err != nil {
		return 0, fmt.Errorf("missionpursuit: counting the checks of the hour: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, err
		}
		if t, perr := time.Parse(time.RFC3339, at); perr != nil || !t.Before(since) {
			n++
		}
	}
	return n, rows.Err()
}

// CheckFacts is what the host step needs to know about the checks of one mission, read from the rows in one go.
type CheckFacts struct {
	Running        bool // a check of the mission is running (a row in state running)
	HaveLatest     bool
	LatestID       int64
	LatestState    string
	LatestOutcome  string
	LatestReason   string
	LatestDigest   string
	LatestVerdict  string
	LatestContain  string    // where the programs ran; for a refused confinement, which self test failed
	LatestStarted  time.Time // zero when it does not read
	AwaitingPerson bool      // the latest check passed its machine criteria and its reviewer and waits for the person's answers
	SinceVerdict   int       // checks after the latest accepted or rejected one
	BadReviewer    int       // checks in a row (across away windows) that ended blocked because the reviewer could not be used
}

// ReadCheckFacts reads the latest checks of a mission (at most 200 rows). The reviewer-failing count runs across away windows; the
// host lifts the stop when the person acted since the latest check. baseID is not used by it (callers pass the window base).
func ReadCheckFacts(ctx context.Context, db *sql.DB, session, baseID int64) (CheckFacts, error) {
	var f CheckFacts
	rows, err := db.QueryContext(ctx, `SELECT id, state, outcome, reason, subject_digest, started_at, reviewer_verdict, containment FROM mission_goal_checks WHERE session_id = ? ORDER BY id DESC LIMIT 200`, session)
	if err != nil {
		return f, fmt.Errorf("missionpursuit: reading the checks: %w", err)
	}
	defer rows.Close()
	badOpen, verdictSeen := true, false
	for rows.Next() {
		var id int64
		var state, outcome, reason, digest, started, verdict, contain string
		if err := rows.Scan(&id, &state, &outcome, &reason, &digest, &started, &verdict, &contain); err != nil {
			return f, err
		}
		if state == CheckRunning {
			f.Running = true
		}
		if !f.HaveLatest {
			f.HaveLatest, f.LatestID, f.LatestState, f.LatestOutcome, f.LatestReason, f.LatestDigest, f.LatestVerdict, f.LatestContain = true, id, state, outcome, reason, digest, verdict, contain
			f.LatestStarted, _ = time.Parse(time.RFC3339, started)
			f.AwaitingPerson = state == CheckDone && outcome == OutcomePassedMachine && verdict == "ok"
		}
		if outcome == OutcomeAccepted || outcome == OutcomeRejected {
			verdictSeen = true
		}
		if !verdictSeen {
			f.SinceVerdict++
		}
		if badOpen && state == CheckDone && outcome == "blocked" && reviewerUnusable(reason) {
			f.BadReviewer++
		} else {
			badOpen = false
		}
	}
	return f, rows.Err()
}

// reviewerUnusable says whether a blocked check's reason is the reviewer being unusable, not the reviewer's verdict. The words are
// the verifier library's reasons (a test holds them equal; this file does not import the library).
func reviewerUnusable(reason string) bool {
	switch reason {
	case "reviewer-unavailable", "reviewer-invalid", "reviewer-pin-changed":
		return true
	}
	return false
}

// CheckStatus is the goal-check line of `pursuit status`: the same readers the host's gate and check step use, so what the
// status shows is what the limits count.
type CheckStatus struct {
	Window   CheckSpend // the checks since the person last acted, and what they cost
	Facts    CheckFacts
	HostHour int // checks of all missions that started in the last hour
}

// ReadCheckStatus reads the check counts of a mission as of now.
func ReadCheckStatus(ctx context.Context, db *sql.DB, session int64, now time.Time) (CheckStatus, error) {
	var base int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT base_check_id FROM mission_pursuit_state WHERE session_id = ?), 0)`, session).Scan(&base); err != nil {
		return CheckStatus{}, fmt.Errorf("missionpursuit: reading the check base: %w", err)
	}
	var out CheckStatus
	var err error
	if out.Window, err = CheckSpendAfter(ctx, db, session, base); err != nil {
		return CheckStatus{}, err
	}
	if out.Facts, err = ReadCheckFacts(ctx, db, session, base); err != nil {
		return CheckStatus{}, err
	}
	if out.HostHour, err = HostChecksSince(ctx, db, now.Add(-time.Hour)); err != nil {
		return CheckStatus{}, err
	}
	return out, nil
}
