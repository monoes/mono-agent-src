package coderhost

import (
	"strings"
	"time"
)

// The close wake. A mission whose every task is settled (done, or verified and waiting in review) has nothing for the lead
// to do but close it, and closing is the lead's alone (integration task, merges, the close proposal). So the loop spends
// ONCE per settled state: one epoch (its judge call) and its one wake-up, both counted and limited as any other. It then
// pays nothing until a task changes. Nothing is stored for this: the settled state is "the newest task change", and
// "already woken" is a pursuit wake-up in the turn journal created at or after it.

// pursuitCloseWakeDue says whether a settled mission is owed its one close wake (the caller has checked that it has a task,
// none blocked and none workable): no close proposed at or after the newest task change, and no pursuit wake-up queued or
// sent at or after it other than selfWakes of them (the wake-up being decided on counts against nobody). A read that fails
// says no: nothing is spent on a guess.
func (s *Store) pursuitCloseWakeDue(m pursuitMission, selfWakes int) bool {
	changed, err := s.newestTaskChange(m.ID)
	if err != nil {
		return false
	}
	var proposed string
	err = s.db.QueryRow(`SELECT COALESCE((SELECT at FROM task_session_events WHERE session_id = ? AND kind = 'close_proposed' ORDER BY id DESC LIMIT 1), '')`, m.ID).Scan(&proposed)
	if err != nil {
		return false
	}
	if proposed != "" {
		if at, perr := time.Parse(time.RFC3339Nano, proposed); perr != nil || !at.Before(changed) {
			return false // a proposal of this state (or one whose time does not read) is outstanding
		}
	}
	rows, err := s.db.Query(`SELECT created_at FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'send' AND substr(turn_id, 1, 8) = 'pursuit-' AND status <> ? LIMIT 1000`,
		m.Profile, m.Conv, CmdFailed)
	if err != nil {
		return false
	}
	defer rows.Close()
	sent := 0
	for rows.Next() {
		var created string
		if rows.Scan(&created) != nil {
			return false
		}
		if at, perr := time.Parse(time.RFC3339Nano, created); perr != nil || !at.Before(changed) {
			sent++
		}
	}
	return rows.Err() == nil && sent-selfWakes <= 0
}

// newestTaskChange is the latest updated_at of the mission's tasks; a time that does not read is ignored.
func (s *Store) newestTaskChange(session int64) (time.Time, error) {
	rows, err := s.db.Query(`SELECT updated_at FROM tasks WHERE session_id = ? LIMIT 100000`, session)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var newest time.Time
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return time.Time{}, err
		}
		if at, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(u)); perr == nil && at.After(newest) {
			newest = at
		}
	}
	return newest, rows.Err()
}
