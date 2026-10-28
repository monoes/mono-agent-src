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
//   - ends a child's row once the child is idle with nothing queued: done when its task is in Review or Done (checked
//     first, so a child that finished and was then interrupted is done), stopped when a stop ran since its last turn
//     began (a person's or its lead's), expired when its first message never ran, budget when its cap is used up,
//     else no_finish; or gone when its conversation is gone. One turn is one attempt: a child that ends without
//     handing its task back is no_finish and its cap is released, and the lead starts a fresh attempt (at most
//     MaxChildAttempts). Each end returns the unspent carve to the lead;
//   - cascades a stopped lead, a mission that was stopped, closed, archived, deleted or handed to another
//     conversation, to its live children: a stop for each, and the row ends as lead_stopped once the child's turn has
//     ended. The lead counts as stopped when its session is marked stopped or when a person's stop (a done stop
//     command with no issuer) ran for it since its last turn began;
//   - queues a pause for the live children of a paused mission (weaker than pausing in-turn workers: a turn that is
//     running finishes under its cap). Nothing resumes them by itself: the lead resumes a child with the master
//     verbs, or the operator does;
//   - gives back reservations whose spawn died (ResetStaleSpawning) and stops the conversations such a spawn left;
//   - reports, once, in the log, the child sessions that hold a carve but belong to no row (FindOrphanCarves). It
//     only reports: a lead may legitimately have children that are in no row, so nothing is stopped on age alone.
//
// A host restart needs nothing beyond Recover: rows are keyed to conversation ids and the pass reads the sessions.

// Timings of the pass.
const (
	// MissionChildEvery is how often the pass runs when Host.MissionChildEvery is not set.
	MissionChildEvery = 5 * time.Second
	// MissionSpawnStale is how long a reservation may go untouched before it is given back.
	MissionSpawnStale = 5 * time.Minute
	// MissionOrphanAge is how old a child with a carve and no row must be before the pass reports it.
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
	h.leadFacts = map[string]leadFact{}
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
	h.settleEnded()
	h.reportOrphanCarves()
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
	closed := !found || (ms.lead != "" && ms.lead != r.LeadConversationID) || ms.archived || ms.state == "stopped" || ms.state == "done" || ms.state == "failed"
	leadStopped := !leadFound || lead.State == StateStopped || h.leadStopSince(r)
	if leadStopped || closed {
		h.stopChild(r.ProfileID, r.LeadConversationID, r.ChildConversationID)
		if x, ok, err := st.GetSession(r.ProfileID, r.ChildConversationID); err == nil && ok && x.TurnID != "" {
			return nil // its turn is being stopped: the row ends on a later pass, once the turn has, so the carve is not released under it
		}
		ended, err := st.End(r.ProfileID, r.ChildConversationID, EndLeadStop)
		if ended {
			h.settleChild(r.ProfileID, r.ChildConversationID)
		}
		return err
	}
	// 2. The child itself.
	exists, err := st.ConversationExists(r.ProfileID, r.ChildConversationID)
	if err != nil {
		return err
	}
	if !exists {
		ended, err := st.MarkGone(r.ProfileID, r.ChildConversationID)
		if ended {
			h.settleChild(r.ProfileID, r.ChildConversationID)
		}
		return err
	}
	cs, ok, err := st.GetSession(r.ProfileID, r.ChildConversationID)
	if err != nil || !ok {
		return err
	}
	if ms.state == "paused" {
		if cs.State != StatePaused && !st.hasPendingCascade(r.ProfileID, r.ChildConversationID, KindPause) {
			if _, err := st.Enqueue(Command{ProfileID: r.ProfileID, ConversationID: r.ChildConversationID, Kind: KindPause, TurnID: cascadeTurn}); err != nil {
				return err
			}
		}
		return nil
	}
	if ms.state == "running" && cs.State == StatePaused && st.pausedByCascade(r.ProfileID, r.ChildConversationID) && !st.hasPendingCascade(r.ProfileID, r.ChildConversationID, KindResume) {
		// the mission runs again and nobody but this pass paused the child: it goes on, with the same host-made command
		if st.betweenCascadeCheckAndResume != nil {
			st.betweenCascadeCheckAndResume()
		}
		_, err := st.enqueueCascadeResume(r.ProfileID, r.ChildConversationID) // refused by the statement itself if a person's pause came in since the check
		return err
	}
	idle := cs.State == StateIdle && cs.TurnID == "" && st.PendingFor(r.ProfileID, r.ChildConversationID) == 0
	if cs.State != StateStopped && !idle {
		return nil // running, paused, waiting for a person or still has a message to take
	}
	reason := EndNoFinish
	switch {
	case st.taskHandedBack(r): // first: a child that finished and was then interrupted is done
		reason = EndDone
	case cs.State == StateStopped || st.stopSince(r.ProfileID, r.ChildConversationID, st.turnFloor(r.ProfileID, r.ChildConversationID, r.SpawnedAt), false):
		reason = EndStopped
	case st.lastTurnID(r.ProfileID, r.ChildConversationID) == "":
		reason = EndExpired // its first message never ran
	default:
		if _, _, err := st.TurnCap(r.ProfileID, r.ChildConversationID); errors.Is(err, errBudgetUsedUp) {
			reason = EndBudget
		}
	}
	ended, err := st.End(r.ProfileID, r.ChildConversationID, reason)
	if err != nil || !ended {
		return err
	}
	h.settleChild(r.ProfileID, r.ChildConversationID)
	h.retireIdle(r.ProfileID, r.ChildConversationID)
	return nil
}

// retireIdle stops a task child's session once its row has ended, so it no longer counts toward its master's
// max_children (the count is every child not stopped) and the next task can have the slot. Only a session
// that is still idle with nothing queued, judged by the write itself, is stopped; a child that is not a task's
// is never reached from here.
func (h *Host) retireIdle(profileID, conv string) {
	if _, err := h.Store.StopIfIdle(profileID, conv); err != nil {
		h.logf("coder host: retiring %s: %v", conv, err)
	}
}

// leadStopSince says whether a person's stop of the row's lead came after both the lead's last turn began and the
// row was started: a stop counts until the lead's next turn begins, and never ends a child spawned after it. The
// lead's two times are read once per lead per pass.
func (h *Host) leadStopSince(r ChildRow) bool {
	if h.leadFacts == nil {
		h.leadFacts = map[string]leadFact{}
	}
	k := key(r.ProfileID, r.LeadConversationID)
	lf, ok := h.leadFacts[k]
	if !ok {
		lf = leadFact{stop: h.Store.latestStop(r.ProfileID, r.LeadConversationID, true), turn: h.Store.turnFloor(r.ProfileID, r.LeadConversationID, "")}
		h.leadFacts[k] = lf
	}
	floor := lf.turn
	if t, err := time.Parse(time.RFC3339Nano, r.SpawnedAt); err == nil && t.After(floor) {
		floor = t
	}
	return lf.stop.After(floor)
}

// leadFact is what the pass reads about a lead, once per pass.
type leadFact struct{ stop, turn time.Time }

// stopChild stops a child's turn and what is queued for it, or, when nothing runs, marks the session stopped.
// The stop is the host's own (no issuer): a stopped lead's issuer would be refused. A session that names a turn
// this host does not have live (a stale turn) has nothing to stop: its turn is cleared instead, so a stop that
// could only fail is not queued again every pass.
func (h *Host) stopChild(profileID, lead, conv string) {
	st := h.Store
	x, ok, err := st.GetSession(profileID, conv)
	if err != nil || !ok || x.State == StateStopped {
		return
	}
	if x.TurnID != "" {
		h.mu.Lock()
		live := h.live[key(profileID, conv)] != nil
		if !live {
			h.L.Finish(profileID, conv, x.TurnID, "the turn's process is gone and its session is being stopped") // as Recover does
			h.settle(x)                                                                                          // clears the stale turn; the session is idle
		}
		h.mu.Unlock()
		if live {
			h.queueCascadeStop(profileID, conv)
			return
		}
		if x, ok, err = st.GetSession(profileID, conv); err != nil || !ok {
			return
		}
	}
	if st.PendingFor(profileID, conv) > 0 {
		h.queueCascadeStop(profileID, conv)
		return
	}
	x.State = StateStopped
	if err := st.SaveSession(x); err != nil {
		h.logf("coder host: stopping %s: %v", conv, err)
	}
}

// queueCascadeStop queues the host's own stop (no issuer, the pass's turn id) unless one is already waiting.
func (h *Host) queueCascadeStop(profileID, conv string) {
	if h.Store.hasPendingCascade(profileID, conv, KindStop) {
		return
	}
	if _, err := h.Store.Enqueue(Command{ProfileID: profileID, ConversationID: conv, Kind: KindStop, TurnID: cascadeTurn}); err != nil {
		h.logf("coder host: queueing a stop for %s: %v", conv, err)
	}
}

// reportOrphanCarves logs, once while they last, the child sessions that hold a carve but are in no mission row:
// what a crash between a spawn's link and its pending note leaves, or a child the lead made with the spawn
// verb. It changes nothing; an operator decides.
func (h *Host) reportOrphanCarves() {
	list, err := h.Store.FindOrphanCarves(MissionOrphanAge)
	if err != nil {
		h.logf("coder host: looking for orphaned carves: %v", err)
		return
	}
	seen := make(map[string]bool, len(list)) // entries of sessions that are gone or no longer orphans drop out
	for _, o := range list {
		k := key(o.ProfileID, o.ConversationID)
		seen[k] = true
		if !h.orphanSeen[k] {
			h.logf("coder host: child session %s of mission lead %s holds a carve of $%.2f and is in no mission row (informational: the lead may have made it with the spawn verb)", o.ConversationID, o.LeadConversationID, o.BudgetUSD)
		}
	}
	h.orphanSeen = seen
}

// ---- reads for the pass (read-only; the writes are the store's methods)

// rowOwnerOf finds the profile and lead of the row that holds the conversation.
func (s *Store) rowOwnerOf(conv string) (profile, lead string, ok bool) {
	err := s.db.QueryRow(`SELECT profile_id, lead_conversation_id FROM coder_mission_children WHERE child_conversation_id = ? OR pending_conversation_id = ? LIMIT 1`, conv, conv).Scan(&profile, &lead)
	return profile, lead, err == nil
}

// hasPending says whether a command of the kind is already waiting for the conversation.
func (s *Store) hasPending(profileID, conv string, kind Kind) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = ? AND status = 'pending'`,
		profileID, conv, string(kind)).Scan(&n) != nil || n > 0
}

// hasPendingCascade says whether the pass's own command of the kind (no issuer, its turn id) is waiting: a stop or
// pause a master queued, which may be held, does not stand in for it.
func (s *Store) hasPendingCascade(profileID, conv string, kind Kind) bool {
	var n int
	return s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = ? AND status = 'pending' AND issuer = '' AND turn_id = ?`,
		profileID, conv, string(kind), cascadeTurn).Scan(&n) != nil || n > 0
}

// pausedByCascade says whether the newest pause or resume that reached the conversation is the pass's own pause,
// carried out. A person's pause or resume after it, or one still waiting, means the pause is not the pass's to undo.
func (s *Store) pausedByCascade(profileID, conv string) bool {
	var status, issuer, turn string
	var id int64
	err := s.db.QueryRow(`SELECT id, kind || ':' || status, issuer, turn_id FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind IN ('pause', 'resume') AND status IN ('pending', 'done')
		ORDER BY id DESC LIMIT 1`, profileID, conv).Scan(&id, &status, &issuer, &turn)
	if err != nil || status != "pause:done" || issuer != "" || turn != cascadeTurn {
		return false
	}
	// A person's pause queued before the pass's own (still waiting when the pass saw the child not yet paused)
	// holds the child too: any other pause since the last resume keeps the pause from being the pass's alone.
	var other int
	if s.db.QueryRow(`SELECT COUNT(*) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'pause' AND status IN ('pending', 'done') AND id < ?
		AND NOT (issuer = '' AND turn_id = ?)
		AND id > COALESCE((SELECT MAX(id) FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'resume' AND status = 'done' AND id < ?), 0)`,
		profileID, conv, id, cascadeTurn, profileID, conv, id).Scan(&other) != nil || other > 0 {
		return false
	}
	return true
}

// turnFloor is the time a stop must be newer than to count for the conversation: when its last turn began, or
// when the row was started if it has none. A stop is a done stop command; the pass's own stops (cascadeTurn) are
// never counted.
func (s *Store) turnFloor(profileID, conv, fallback string) time.Time {
	var at string
	if s.db.QueryRow(`SELECT created_at FROM ai_chat_turns WHERE profile_id = ? AND conversation_id = ? ORDER BY created_at DESC LIMIT 1`, profileID, conv).Scan(&at) == nil {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			return t
		}
	}
	t, _ := time.Parse(time.RFC3339Nano, fallback)
	return t
}

// latestStop is when the latest done stop command for the conversation became done (the host stamps a command's
// updated_at when it ends it), or the zero time. A stop is dated by when it completed, not by when it was queued: one
// queued while a turn was being admitted, before its turn row exists, still stopped that turn. unissuedOnly counts only
// a person's stop (no issuer): the lead is stopped by its operator, not by whoever else can queue a command for it.
// (An unissued stop row is operator-equivalent: only something that can queue the operator's own commands could forge
// one.) The pass's own stops (cascadeTurn) are never counted. The query reads the newest few rows through the index on
// (profile, conversation, kind).
func (s *Store) latestStop(profileID, conv string, unissuedOnly bool) time.Time {
	q := `SELECT updated_at FROM coder_session_commands WHERE profile_id = ? AND conversation_id = ? AND kind = 'stop' AND status = 'done' AND turn_id <> ?`
	if unissuedOnly {
		q += ` AND issuer = ''`
	}
	rows, err := s.db.Query(q+` ORDER BY id DESC LIMIT 20`, profileID, conv, cascadeTurn)
	if err != nil {
		return time.Time{}
	}
	defer rows.Close()
	var latest time.Time
	for rows.Next() {
		var at string
		if rows.Scan(&at) != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && t.After(latest) {
			latest = t
		}
	}
	return latest
}

// stopSince says whether a stop completed for the conversation after the floor (see latestStop).
func (s *Store) stopSince(profileID, conv string, floor time.Time, unissuedOnly bool) bool {
	return s.latestStop(profileID, conv, unissuedOnly).After(floor)
}

func (s *Store) lastTurnID(profileID, conv string) string {
	id, _ := s.LastTurn(profileID, conv)
	return id
}

// taskHandedBack says whether the row's task is in Review or Done (read-only).
func (s *Store) taskHandedBack(r ChildRow) bool {
	var status string
	err := s.db.QueryRow(`SELECT status FROM tasks WHERE id = ? AND profile_id = ? AND session_id = ?`, r.TaskID, r.ProfileID, r.SessionID).Scan(&status)
	return err == nil && (status == "review" || status == "done")
}
