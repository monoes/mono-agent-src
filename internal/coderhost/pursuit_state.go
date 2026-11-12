package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// The pursuit loop's remembered state (migrations 167, 168). It is written by column groups, each with an UPDATE of its own
// columns only, because two goroutines write the row (the pass, and the wake-up's re-check when it runs) and a write of every
// column from an earlier read would lose the other's change (a failed-round count, a wake-up count).

// pursuitState is what the loop remembers between ticks (migration 167).
type pursuitState struct {
	SessionID      int64
	PersonAt       time.Time
	BaseSpentMicro int64
	Waiting        string
	WokenEpoch     int64
	WakesSince     int // wake-ups queued since the person last acted
	// shown by the status (pursuit progress), written with the rest when any of them changes
	Parked      int
	SpentAway   int64 // micro-USD of metered spend since the person last acted
	BudgetMicro int64
	HourWakes   int
	HourFreesAt string
	// the judge and the lead's turns (migration 168)
	BadRounds     int    // judge rounds in a row that failed
	BaseJudgeID   int64  // the judge ledger's highest id when the person last acted
	BaseLeadMicro int64  // the lead's and its helpers' turn-journal spend then
	JudgeMicro    int64  // judge spend since the person last acted (shown)
	LeadMicro     int64  // lead and helper turn spend since then (shown)
	JudgeCalls    int    // judge calls since then (shown)
	HourJudges    int    // judge calls of all missions in the last hour (shown)
	LastRound     string // how the latest judge round ended
	// the goal checks (migration 169): the check ledger's highest id when the person last acted
	BaseCheckID int64
}

// usdMicro is an amount in micro-USD, clamped so it can neither overflow nor go negative.
func usdMicro(usd float64) int64 {
	if math.IsNaN(usd) || usd <= 0 {
		return 0
	}
	if usd > 1e12 {
		return int64(1e12 * 1e6)
	}
	return int64(math.Round(usd * 1e6))
}

// pursuitStateOf is the loop's remembered state of a mission.
func (s *Store) pursuitStateOf(session int64) (pursuitState, bool, error) {
	st := pursuitState{SessionID: session}
	var at string
	err := s.db.QueryRow(`SELECT person_at, base_spent_micro, waiting, woken_epoch, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at,
		bad_rounds, base_judge_id, base_lead_micro, judge_micro, lead_micro, judge_calls, hour_judges, last_round, base_check_id FROM mission_pursuit_state WHERE session_id = ?`, session).
		Scan(&at, &st.BaseSpentMicro, &st.Waiting, &st.WokenEpoch, &st.WakesSince, &st.Parked, &st.SpentAway, &st.BudgetMicro, &st.HourWakes, &st.HourFreesAt,
			&st.BadRounds, &st.BaseJudgeID, &st.BaseLeadMicro, &st.JudgeMicro, &st.LeadMicro, &st.JudgeCalls, &st.HourJudges, &st.LastRound, &st.BaseCheckID)
	if errors.Is(err, sql.ErrNoRows) {
		return pursuitState{SessionID: session}, false, nil
	}
	if err != nil {
		return pursuitState{}, false, fmt.Errorf("reading the pursuit state: %w", err)
	}
	if at != "" {
		if st.PersonAt, err = time.Parse(time.RFC3339Nano, at); err != nil {
			return pursuitState{}, false, fmt.Errorf("the pursuit state's time does not read: %w", err)
		}
	}
	return st, true, nil
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// insertPursuitState writes the first record of a mission's pursuit; an existing row is left as it is.
func (s *Store) insertPursuitState(st pursuitState, now time.Time) error {
	_, err := s.db.Exec(`INSERT INTO mission_pursuit_state (session_id, person_at, base_spent_micro, waiting, wakes_since, parked, spent_away_micro, budget_micro, hour_wakes, hour_frees_at,
		bad_rounds, base_judge_id, base_lead_micro, judge_micro, lead_micro, judge_calls, hour_judges, last_round, base_check_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO NOTHING`,
		st.SessionID, fmtTime(st.PersonAt), st.BaseSpentMicro, st.Waiting, st.WakesSince, st.Parked, st.SpentAway, st.BudgetMicro, st.HourWakes, st.HourFreesAt,
		st.BadRounds, st.BaseJudgeID, st.BaseLeadMicro, st.JudgeMicro, st.LeadMicro, st.JudgeCalls, st.HourJudges, st.LastRound, st.BaseCheckID, fmtTime(now))
	if err != nil {
		return fmt.Errorf("saving the pursuit state: %w", err)
	}
	return nil
}

// updateGateState writes what the gate measured: why the pursuit waits and the numbers the status shows. When the person has
// acted (reset) it also starts the counters again; otherwise it leaves wakes_since, bad_rounds, last_round and woken_epoch alone,
// which other writers move.
func (s *Store) updateGateState(st pursuitState, reset bool, now time.Time) error {
	var err error
	if reset {
		_, err = s.db.Exec(`UPDATE mission_pursuit_state SET person_at = ?, base_spent_micro = ?, base_judge_id = ?, base_lead_micro = ?, base_check_id = ?, wakes_since = 0, bad_rounds = 0, last_round = '' WHERE session_id = ?`,
			fmtTime(st.PersonAt), st.BaseSpentMicro, st.BaseJudgeID, st.BaseLeadMicro, st.BaseCheckID, st.SessionID)
	}
	if err == nil {
		_, err = s.db.Exec(`UPDATE mission_pursuit_state SET waiting = ?, parked = ?, spent_away_micro = ?, budget_micro = ?, hour_wakes = ?, hour_frees_at = ?,
			judge_micro = ?, lead_micro = ?, judge_calls = ?, hour_judges = ?, updated_at = ? WHERE session_id = ?`,
			st.Waiting, st.Parked, st.SpentAway, st.BudgetMicro, st.HourWakes, st.HourFreesAt, st.JudgeMicro, st.LeadMicro, st.JudgeCalls, st.HourJudges, fmtTime(now), st.SessionID)
	}
	if err != nil {
		return fmt.Errorf("saving the pursuit state: %w", err)
	}
	return nil
}

// recordJudgeRound counts a judge round that ended: a good one starts the failed-in-a-row count again, a failed one adds to it.
func (s *Store) recordJudgeRound(session int64, good bool, round string, now time.Time) error {
	q := `UPDATE mission_pursuit_state SET bad_rounds = bad_rounds + 1, last_round = ?, updated_at = ? WHERE session_id = ?`
	if good {
		q = `UPDATE mission_pursuit_state SET bad_rounds = 0, last_round = ?, updated_at = ? WHERE session_id = ?`
	}
	if _, err := s.db.Exec(q, round, fmtTime(now), session); err != nil {
		return fmt.Errorf("saving the judge round: %w", err)
	}
	return nil
}

// setPursuitWaiting records why the pursuit waits. With onlyIfFree it never replaces a reason already there. A mission the loop
// has not recorded yet gets a first row.
func (s *Store) setPursuitWaiting(session int64, why string, onlyIfFree bool, now time.Time) error {
	if _, err := s.db.Exec(`INSERT INTO mission_pursuit_state (session_id, waiting, updated_at) VALUES (?, ?, ?) ON CONFLICT(session_id) DO NOTHING`, session, why, fmtTime(now)); err != nil {
		return fmt.Errorf("saving the pursuit state: %w", err)
	}
	q := `UPDATE mission_pursuit_state SET waiting = ?, updated_at = ? WHERE session_id = ? AND waiting <> ?`
	if onlyIfFree {
		q = `UPDATE mission_pursuit_state SET waiting = ?, updated_at = ? WHERE session_id = ? AND waiting = '' AND ? <> ''`
	}
	if _, err := s.db.Exec(q, why, fmtTime(now), session, why); err != nil {
		return fmt.Errorf("saving the pursuit state: %w", err)
	}
	return nil
}
