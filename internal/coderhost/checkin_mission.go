package coderhost

import (
	"database/sql"
	"fmt"

	"github.com/monoes/mono-agent/internal/tasks"
)

// missionFact is the progress of the task session a master's conversation is
// working (a mission), as the check-in digest shows it. Like the rest of the
// digest it is the host's own record: a state word and counts. The session's
// title, goal and every task's text are agent-written and never reach the digest.
type missionFact struct {
	State    string `json:"state"`
	Done     int    `json:"done"`
	Review   int    `json:"review"`
	Total    int    `json:"total"`
	Verified int    `json:"verified"`
	Blocked  int    `json:"blocked"`
	// Open is the work a lead can move: tasks not started or in progress, plus tasks handed back to review
	// that nobody has passed and that are not blocked. It is computed from disjoint statuses (a task keeps
	// verified=1 after the operator closes it, so Done and Verified overlap and must not be summed). It is
	// not part of the digest or of the change signal.
	Open int `json:"-"`
}

// missionOf is the open (draft, running or paused) task session bound to the conversation, or nil. ok is
// false when the read failed (the caller keeps what it had). It is read-only and asks the database only
// for this profile's sessions of this conversation, not for every session of the profile.
func (h *Host) missionOf(profileID, conversationID string) (m *missionFact, ok bool) {
	db := h.Store.db
	var id int64
	var state string
	err := db.QueryRow(`SELECT id, state FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = ''
		AND state IN ('draft','running','paused') ORDER BY id DESC LIMIT 1`, profileID, conversationID).Scan(&id, &state)
	if err == sql.ErrNoRows {
		return nil, true
	}
	if err != nil {
		return nil, false
	}
	rows, err := db.Query(`SELECT status, blocked, verified, COUNT(*) FROM tasks WHERE session_id = ? AND status <> 'archived' AND kind <> 'epic'
		GROUP BY status, blocked, verified`, id)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	f := &missionFact{State: state}
	for rows.Next() {
		var st string
		var blocked, verified, n int
		if err := rows.Scan(&st, &blocked, &verified, &n); err != nil {
			return nil, false
		}
		f.Total += n
		if blocked == 1 {
			f.Blocked += n
		}
		if verified == 1 {
			f.Verified += n
		}
		switch tasks.Status(st) {
		case tasks.StatusDone:
			f.Done += n
		case tasks.StatusReview:
			f.Review += n
			if verified == 0 && blocked == 0 {
				f.Open += n // handed back, nobody has passed it yet
			}
		case tasks.StatusInbox, tasks.StatusReady, tasks.StatusInProgress:
			f.Open += n
		}
	}
	if rows.Err() != nil {
		return nil, false
	}
	return f, true
}

// missionLine is the digest's line for a mission: numbers and a state word only.
func missionLine(m *missionFact) string {
	if m == nil {
		return ""
	}
	return fmt.Sprintf("Mission (your task session): %s; %d of %d task(s) done, %d in review, %d verified, %d blocked. Closing tasks and approving a plan stay the operator's.\n",
		m.State, m.Done, m.Total, m.Review, m.Verified, m.Blocked)
}

// open says whether a running mission still has work the lead can move (see missionFact.Open). A blocked task
// waits for the operator and a verified one for the operator's close: nothing a lead does hastens either.
func (m *missionFact) open() bool {
	return m != nil && m.State == string(tasks.SessionRunning) && m.Open > 0
}

// leadWithOpenWork is the rule that lets a session without children be checked in on: its
// conversation is bound to a RUNNING task session (not draft, not paused) that has open work. A draft
// session waits for the person's plan approval and a paused one for the person to resume it; neither is
// woken. A failed read is "no": nothing is queued on a guess.
func (h *Host) leadWithOpenWork(profileID, conversationID string) bool {
	m, ok := h.missionOf(profileID, conversationID)
	return ok && m.open()
}

// liveChildren counts the host-made children of a session that are not stopped, as the check-in pass sees them.
func (h *Host) liveChildren(profileID, conversationID string) (int, error) {
	rows, err := h.Store.db.Query(`SELECT conversation_id FROM coder_sessions WHERE profile_id = ? AND parent_conversation_id = ? AND state <> ?`,
		profileID, conversationID, string(StateStopped))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, err
		}
		if hostID.MatchString(id) {
			n++
		}
	}
	return n, rows.Err()
}

// checkinTargetStillValid is the one rule for who may be checked in on, used when a check-in is looked at,
// when it is about to be queued and when it is about to run. It returns why not, or "". The session
// must be unattended and not a child; with live children it must be a master; with none (a lead) its
// conversation must be bound to a RUNNING task session with open work.
func (h *Host) checkinTargetStillValid(x Session, liveChildren int) string {
	switch {
	case !x.Unattended:
		return "the session is no longer an unattended master"
	case x.Role == "child":
		return "the session is no longer an unattended master"
	case liveChildren > 0:
		if x.Role != "master" {
			return "the session is no longer an unattended master"
		}
	case !h.leadWithOpenWork(x.ProfileID, x.ConversationID):
		return "the session has no running mission with open work"
	}
	return ""
}
