package coderhost

import (
	"errors"
	"time"
)

// The host's half of mission child sessions (phase P-B1 of
// docs/mastermind/plans/2026-10-27-mission-child-sessions.md): a reconciler that only observes, cascades and cleans
// up. It never claims, finishes, verifies or approves a task and writes no task row; it reads the tasks table to
// tell a child that finished its task from one that did not. What it does, once per pass:
//
//   - ends a child's row when its turn has ended (done when its task is in Review or Done, budget when its cap is
//     used up, else no_finish), when the operator stopped it (stopped), when its first message never ran
//     (expired) or when its conversation is gone (gone). Each end returns the unspent carve to the lead;
//   - cascades a stopped lead, a stopped, closed or archived mission, or a deleted mission to its live children:
//     a stop for each, and the row ends as lead_stopped;
//   - queues a pause for the live children of a paused mission (weaker than pausing in-turn workers: a turn that is
//     running finishes under its cap). Nothing resumes them by itself: the lead resumes a child with the master
//     verbs, or the operator does;
//   - gives back reservations whose spawn died (ResetStaleSpawning) and stops the conversations such a spawn left;
//   - stops, and logs, a crash orphan: a child session of a lead that has a policy, with no row, no turn, no turn
//     ever, nothing queued, idle for MissionOrphanAge. It stops the session and deletes nothing.
//
// A host restart needs nothing beyond Recover: rows are keyed to conversation ids and the pass reads the sessions.

// Timings of the pass.
const (
	// MissionChildEvery is how often the pass runs when Host.MissionChildEvery is not set.
	MissionChildEvery = 5 * time.Second
	// MissionSpawnStale is how long a reservation may go untouched before it is given back.
	MissionSpawnStale = 5 * time.Minute
	// MissionOrphanAge is how long a child with no row, no turn and no message is left before it is stopped.
	MissionOrphanAge = 10 * time.Minute

	// cascadeTurn is the turn id of the stops and pauses the pass queues, so they are not taken for the
	// operator's.
	cascadeTurn = "mission-cascade"
)

// missionChildPassIfDue runs the pass when MissionChildEvery has passed since the last.
func (h *Host) missionChildPassIfDue() {
	every := h.MissionChildEvery
	if every <= 0 {
		every = MissionChildEvery
	}
	if now := h.Store.now(); now.Sub(h.lastMissionPass) >= every {
		h.lastMissionPass = now
		h.missionChildPass()
	}
}

// missionChildPass is one pass. A failure on one row is logged and the rest go on; the next pass tries again.
func (h *Host) missionChildPass() {
	st := h.Store
	reset, ended, err := st.ResetStaleSpawning(MissionSpawnStale)
	if err != nil {
		h.logf("coder host: mission child reservations: %v", err)
	}
	if reset > 0 {
		h.logf("coder host: gave back %d mission child reservation(s) whose spawn did not finish", reset)
	}
	for _, conv := range ended {
		if profile, lead, ok := st.rowOwnerOf(conv); ok {
			h.logf("coder host: stopping %s, left by a mission child spawn that did not finish", conv)
			h.stopChild(profile, lead, conv)
		}
	}
	rows, err := st.childrenWhere(`state = 'spawned'`)
	if err != nil {
		h.logf("coder host: mission children: %v", err)
		return
	}
	for _, r := range rows {
		if err := h.reconcileChild(r); err != nil {
			h.logf("coder host: mission child %s: %v", r.ChildConversationID, err)
		}
	}
	h.reapMissionOrphans()
}

// reconcileChild looks at one live row.
func (h *Host) reconcileChild(r ChildRow) error {
	st := h.Store
	// 1. The lead and the mission: a child never outlives them.
	lead, leadFound, err := st.GetSession(r.ProfileID, r.LeadConversationID)
	if err != nil {
		return err
	}
	ms, found, err := st.loadMissionSession(r.ProfileID, r.SessionID)
	if err != nil {
		return err
	}
	closed := !found || ms.archived || ms.state == "stopped" || ms.state == "done" || ms.state == "failed"
	if !leadFound || lead.State == StateStopped || closed {
		h.stopChild(r.ProfileID, r.LeadConversationID, r.ChildConversationID)
		if x, ok, err := st.GetSession(r.ProfileID, r.ChildConversationID); err == nil && ok && x.TurnID != "" {
			return nil // its turn is being stopped: the row ends on a later pass, once the turn has, so the carve is not released under it
		}
		_, err := st.End(r.ProfileID, r.ChildConversationID, EndLeadStop)
		return err
	}
	// 2. The child itself.
	exists, err := st.ConversationExists(r.ProfileID, r.ChildConversationID)
	if err != nil {
		return err
	}
	if !exists {
		_, err := st.MarkGone(r.ProfileID, r.ChildConversationID)
		return err
	}
	cs, ok, err := st.GetSession(r.ProfileID, r.ChildConversationID)
	if err != nil || !ok {
		return err
	}
	if ms.state == "paused" {
		if cs.State != StatePaused && !st.hasPending(r.ProfileID, r.ChildConversationID, KindPause) {
			if _, err := st.Enqueue(Command{ProfileID: r.ProfileID, ConversationID: r.ChildConversationID, Kind: KindPause, TurnID: cascadeTurn}); err != nil {
				return err
			}
		}
		return nil
	}
	if cs.State == StateStopped || st.stoppedByOperator(r) {
		_, err := st.End(r.ProfileID, r.ChildConversationID, EndStopped)
		return err
	}
	if cs.State != StateIdle || cs.TurnID != "" || st.PendingFor(r.ProfileID, r.ChildConversationID) > 0 {
		return nil // running, paused, waiting for a person or still has a message to take
	}
	if id, _ := st.LastTurn(r.ProfileID, r.ChildConversationID); id == "" {
		_, err := st.End(r.ProfileID, r.ChildConversationID, EndExpired) // its first message never ran
		return err
	}
	reason := EndNoFinish
	if st.taskHandedBack(r) {
		reason = EndDone
	} else if _, _, err := st.TurnCap(r.ProfileID, r.ChildConversationID); errors.Is(err, errBudgetUsedUp) {
		reason = EndBudget
	}
	_, err = st.End(r.ProfileID, r.ChildConversationID, reason)
	return err
}

// stopChild stops a child's turn and what is queued for it, or, when nothing runs, marks the session stopped.
// The stop is the host's own (no issuer): a stopped lead's issuer would be refused.
func (h *Host) stopChild(profileID, lead, conv string) {
	st := h.Store
	x, ok, err := st.GetSession(profileID, conv)
	if err != nil || !ok || x.State == StateStopped {
		return
	}
	if x.TurnID != "" || st.PendingFor(profileID, conv) > 0 {
		if !st.hasPending(profileID, conv, KindStop) {
			if _, err := st.Enqueue(Command{ProfileID: profileID, ConversationID: conv, Kind: KindStop, TurnID: cascadeTurn}); err != nil {
				h.logf("coder host: queueing a stop for %s: %v", conv, err)
			}
		}
		return
	}
	x.State = StateStopped
	if err := st.SaveSession(x); err != nil {
		h.logf("coder host: stopping %s: %v", conv, err)
	}
}

// reapMissionOrphans stops the child sessions a crash left: a child of a lead that has a policy, in no row, that
// never ran a turn, has nothing queued and has been idle for MissionOrphanAge. It stops and logs; it deletes
// nothing, and it leaves every other child (one the lead made with the spawn verb and has already used) alone.
func (h *Host) reapMissionOrphans() {
	st := h.Store
	sessions, err := st.ListSessions("")
	if err != nil {
		h.logf("coder host: looking for orphans: %v", err)
		return
	}
	old := st.now().Add(-MissionOrphanAge)
	for _, x := range sessions {
		if x.Role != "child" || x.Parent == "" || x.State != StateIdle || x.TurnID != "" {
			continue
		}
		if t, err := time.Parse(timeLayout, x.CreatedAt); err != nil || t.After(old) {
			continue
		}
		if !st.hasMissionPolicy(x.ProfileID, x.Parent) || st.inMissionRow(x.ProfileID, x.ConversationID) {
			continue
		}
		if id, _ := st.LastTurn(x.ProfileID, x.ConversationID); id != "" || st.PendingFor(x.ProfileID, x.ConversationID) > 0 {
			continue
		}
		h.logf("coder host: stopping %s, a child of mission lead %s that no task owns and that never ran", x.ConversationID, x.Parent)
		x.State = StateStopped
		if err := st.SaveSession(x); err != nil {
			h.logf("coder host: stopping %s: %v", x.ConversationID, err)
		}
	}
}

// ---- reads for the pass (read-only; the writes are the store's methods)

// rowOwnerOf finds the profile and lead of the row that holds the conversation.
func (s *Store) rowOwnerOf(conv string) (profile, lead string, ok bool) {
	err := s.db.QueryRow(`SELECT profile_id, lead_conversation_id FROM coder_mission_children WHERE child_conversation_id = ? OR pending_conversation_id = ? LIMIT 1`, conv, conv).Scan(&profile, &lead)
	return profile, lead, err == nil
}

func (s *Store) hasMissionPolicy(profileID, lead string) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_policy WHERE profile_id = ? AND lead_conversation_id = ?`, profileID, lead).Scan(&n) == nil && n > 0
}

func (s *Store) inMissionRow(profileID, conv string) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_mission_children WHERE profile_id = ? AND (child_conversation_id = ? OR pending_conversation_id = ?)`, profileID, conv, conv).Scan(&n) != nil || n > 0
}

// hasPending says whether a command of the kind is already waiting for the conversation.
func (s *Store) hasPending(profileID, conv string, kind Kind) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = ? AND status = 'pending'`,
		profileID, conv, string(kind)).Scan(&n) != nil || n > 0
}

// stoppedByOperator says whether a person's stop ran for the child since it was started: a stop with no issuer
// that is not one of the pass's own.
func (s *Store) stoppedByOperator(r ChildRow) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'stop' AND status = 'done'
		AND issuer = '' AND turn_id <> ? AND created_at >= ?`, r.ProfileID, r.ChildConversationID, cascadeTurn, r.SpawnedAt).Scan(&n) == nil && n > 0
}

// taskHandedBack says whether the row's task is in Review or Done (read-only).
func (s *Store) taskHandedBack(r ChildRow) bool {
	var status string
	err := s.db.QueryRow(`SELECT status FROM tasks WHERE id = ? AND profile_id = ? AND session_id = ?`, r.TaskID, r.ProfileID, r.SessionID).Scan(&status)
	return err == nil && (status == "review" || status == "done")
}
