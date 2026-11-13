package coderhost

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/missionpursuit"
	"github.com/monoes/mono-agent/internal/missionpursuit/inputs"
)

// The pursuit's readers of "is this still a started pursuit, and may a queued wake-up still run" (split from pursuit.go).

// sessionPursuing says whether the person started the pursuit of the mission session (paused or not: a paused pursuit
// still wakes nothing but the person's resume).
func (h *Host) sessionPursuing(session int64) (bool, error) {
	if !h.Pursuit.ready() {
		return false, nil // no loop is wired: the check-ins go on
	}
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM mission_pursuit WHERE session_id = ? AND enabled = 1`, session).Scan(&n)
	return n > 0, err
}

// conversationPursuing says whether the conversation is the lead of a mission whose pursuit is started. A read that
// fails says no: the pursuit loop reads the same rows and cannot wake the lead either, so nothing is woken twice, and a
// check-in's own failed-read handling stays as it was.
func (h *Host) conversationPursuing(profileID, conversationID string) bool {
	if !h.Pursuit.ready() {
		return false
	}
	var n int
	err := h.Store.db.QueryRow(`SELECT COUNT(*) FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1`, profileID, conversationID).Scan(&n)
	return err == nil && n > 0
}

// pursuitWakeStillAllowed re-checks, when a wake-up is about to run, that the pursuit still runs. It returns why not, or "".
func (h *Host) pursuitWakeStillAllowed(x Session, turnID string) string {
	if !h.Pursuit.ready() {
		return errNoPursuitLoop.Error() // a wake-up queued before this host restarted with the loop off
	}
	if check := acceptWakeCheck(turnID); check != 0 {
		if why := h.pursuitAcceptStillDue(x, check); why != "" {
			return why
		}
	}
	m := pursuitMission{Profile: x.ProfileID, Conv: x.ConversationID}
	err := h.Store.db.QueryRow(`SELECT s.id FROM task_sessions s JOIN mission_pursuit p ON p.session_id = s.id
		WHERE s.profile_id = ? AND s.conversation_id = ? AND s.archived_at = '' AND p.enabled = 1 ORDER BY s.id DESC LIMIT 1`, x.ProfileID, x.ConversationID).Scan(&m.ID)
	if err != nil {
		return "the session is no longer the lead of a started pursuit"
	}
	ctx := context.Background()
	// the run state AND the away limits, read now: a wake-up queued before a limit was reached does not run after it
	g, ok := h.pursuitGate(ctx, inputs.New(h.Store.db), m, 1) // the queued wake-up is already in wakes_since: it does not count against itself
	if !ok {
		return "the mission's pursuit is paused or stopped"
	}
	if g.st.Waiting != "" {
		return missionpursuit.WaitingWhy(g.st.Waiting)
	}
	return h.pursuitStillDeliverable(x, g)
}

// pursuitStillDeliverable is the run-time half of the wake-up's conditions, the ones that can change while it waits: the
// lead unattended, inside its active hours, the mission with open work, and the host-made turns in a row below the limit.
// (The lead being free is the host's own queue order; the hourly cap was decided when the wake-up was queued.)
func (h *Host) pursuitStillDeliverable(x Session, g pursuitGateResult) string {
	lead, found, err := h.Store.GetSession(x.ProfileID, x.ConversationID)
	switch {
	case err != nil || !found:
		return readFailed + "the lead's session does not read"
	case !lead.Unattended:
		return "the lead is not unattended"
	}
	settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
	switch {
	case err != nil:
		return readFailed + "the session's settings do not read: " + err.Error()
	case !withinActiveHours(settings.ActiveHours, h.Store.now()):
		return "outside the lead's active hours"
	}
	if t := g.snap.Tasks; t.Workable <= 0 && !g.closeWakeDue && !g.parkWakeDue && !g.acceptWakeDue {
		return "the mission has no open work"
	}
	return h.pursuitStreakStop(x.ProfileID, x.ConversationID, settings)
}

// pursuitWakesLastHour counts the pursuit wake-ups of every mission queued in the last hour (any status: a lost one was
// still paid for in the lead's attention) and says when the oldest leaves the hour. An unreadable time counts.
func (s *Store) pursuitWakesLastHour() (int, string, error) {
	since := s.now().Add(-time.Hour)
	rows, err := s.db.Query(`SELECT created_at FROM coder_session_commands WHERE kind = 'send' AND substr(turn_id, 1, 8) = 'pursuit-' AND ` + tsInWindow("created_at", since) + ` LIMIT 1000`)
	if err != nil {
		return 0, "", fmt.Errorf("counting the pursuit wake-ups of the hour: %w", err)
	}
	defer rows.Close()
	n := 0
	var oldest time.Time
	for rows.Next() {
		var at string
		if err := rows.Scan(&at); err != nil {
			return 0, "", err
		}
		n++
		if t, perr := time.Parse(time.RFC3339Nano, at); perr == nil && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	if oldest.IsZero() {
		return n, "", rows.Err()
	}
	return n, oldest.Add(time.Hour).UTC().Format(time.RFC3339), rows.Err()
}

// PursuitHoldsCheckins says whether the check-ins of the conversation are held back for the pursuit loop. They are only
// when the loop is wired into this host (Host.Pursuit set) AND the conversation leads a mission whose pursuit is started:
// a host without the loop behaves as if no pursuit were started. Production wiring of Host.Pursuit is deferred to the slice
// that brings the real judge (S5); the production-wiring test in internal/cli/chat pins that until then it is nil.
func (h *Host) PursuitHoldsCheckins(profileID, conversationID string) bool {
	return h.conversationPursuing(profileID, conversationID)
}
