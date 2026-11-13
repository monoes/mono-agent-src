package coderhost

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// A wake-up whose turn could not start (the launch was refused or failed) spent nothing: it is not counted against the mission's
// wake-ups or the host's wake-ups of the hour, and its retry waits, doubling each time, so a launch that keeps failing is not retried
// at the host's tick rate. The tries of one epoch stay bounded by maxWakeTries.
var (
	wakeRetryBase = 30 * time.Second
	wakeRetryMax  = 5 * time.Minute
)

// unstartedFailedSQL matches a command row that failed before its turn existed (a failure after the start carries startedMarker and
// did run). Expired sends match too: they never ran.
const unstartedFailedSQL = "(status = 'failed' AND error NOT LIKE '" + startedMarker + "%')"

// wakeRetryAt says when the next wake-up of the epoch (or check) whose turn ids match pattern may be queued: the last lost one's end plus
// a doubling wait. The zero time means no wake-up was lost, so none waits.
func (s *Store) wakeRetryAt(q querier, m pursuitMission, pattern string) (time.Time, error) {
	rows, err := q.Query(`SELECT updated_at FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND turn_id LIKE ? AND status = 'failed'`,
		m.Profile, m.Conv, pattern)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	lost := 0
	var last time.Time
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return time.Time{}, err
		}
		lost++
		if t, perr := time.Parse(timeLayout, at); perr == nil && t.After(last) {
			last = t
		}
	}
	if err := rows.Err(); err != nil || lost == 0 {
		return time.Time{}, err
	}
	wait := wakeRetryBase
	for i := 1; i < lost && wait < wakeRetryMax; i++ {
		wait *= 2
	}
	return last.Add(min(wait, wakeRetryMax)), nil
}

// refundWake gives back the mission's wake-up count for a wake-up command that ended without its turn starting. The wake-up was counted
// when it was queued; a command that failed with the turn started, or that is not a pursuit wake-up, is left alone.
func (s *Store) refundWake(c Command, errText string) error {
	if c.Kind != KindSend || !strings.HasPrefix(c.TurnID, PursuitTurnPrefix) || strings.HasPrefix(errText, startedMarker) {
		return nil
	}
	var id int64
	err := s.db.QueryRow(`SELECT s.id FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1 ORDER BY s.id DESC LIMIT 1`, c.ProfileID, c.ConversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // not the lead of a started pursuit: nothing was counted
	}
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE mission_pursuit_state SET wakes_since = MAX(wakes_since - 1, 0) WHERE session_id = ?`, id)
	return err
}
