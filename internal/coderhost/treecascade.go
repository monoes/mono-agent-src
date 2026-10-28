package coderhost

// The tree cascade: a session that is stopped, or whose person's stop ran since its last turn began, takes every
// descendant down with it, and a paused session has its descendants paused, at any depth of the tree (the design
// allows four levels). The mission child pass (missionchild.go) already does this for the task children of a mission
// lead; this pass covers the sessions made with the master tools, whatever level they sit at, and uses the same
// stop (stopChild) and the same pause turn (cascadeTurn), so the two never disagree.
//
// A pause is the weaker half, as there: a running turn finishes under its cap. When no ancestor is paused any more, a
// descendant the pass itself paused is resumed; one a person or a master paused stays paused. A stop is final for the
// descendant.

// treeNode is what the pass reads about one session.
type treeNode struct{ profile, conv, parent, state string }

// treeCascadePass is one pass over every session that has a parent and is not stopped. It runs on every Tick: the
// read is one query over the sessions that have a parent, none in a host with no child sessions. A failure is logged and the
// next pass tries again.
func (h *Host) treeCascadePass() {
	st := h.Store
	rows, err := st.db.Query(`SELECT profile_id, conversation_id, parent_conversation_id, state FROM coder_sessions WHERE parent_conversation_id != ''`)
	if err != nil {
		h.logf("coder host: tree cascade: %v", err)
		return
	}
	nodes := map[string]treeNode{}
	var live []treeNode
	for rows.Next() {
		var p, c, par, state string
		if err := rows.Scan(&p, &c, &par, &state); err != nil {
			rows.Close()
			h.logf("coder host: tree cascade: %v", err)
			return
		}
		n := treeNode{profile: p, conv: c, parent: par, state: state}
		nodes[key(p, c)] = n
		if State(state) != StateStopped {
			live = append(live, n)
		}
	}
	rows.Close()
	stops := map[string]bool{} // per ancestor, read once per pass
	for _, n := range live {
		stop, pause := h.ancestorFate(nodes, n.profile, n.conv, stops)
		switch {
		case stop:
			h.stopChild(n.profile, "", n.conv)
		case pause && State(n.state) != StatePaused:
			h.queueCascade(n.profile, n.conv, KindPause)
		case !pause && State(n.state) == StatePaused && st.pausedByCascade(n.profile, n.conv):
			h.queueCascade(n.profile, n.conv, KindResume) // the pause was this pass's own: lifted with the ancestor's
		}
	}
}

// queueCascade queues the host's own pause or resume (no issuer, the pass's turn id) unless one is already waiting.
func (h *Host) queueCascade(profile, conv string, kind Kind) {
	if h.Store.hasPendingCascade(profile, conv, kind) {
		return
	}
	if _, err := h.Store.Enqueue(Command{ProfileID: profile, ConversationID: conv, Kind: kind, TurnID: cascadeTurn}); err != nil {
		h.logf("coder host: queueing a %s for %s: %v", kind, conv, err)
	}
}

// ancestorFate walks up from conv and says whether an ancestor is stopped (or was stopped by a person since its last
// turn began) and whether one is paused. The walk is bounded, so a damaged tree with a loop ends.
func (h *Host) ancestorFate(nodes map[string]treeNode, profile, conv string, stops map[string]bool) (stop, pause bool) {
	cur := conv
	for i := 0; i < maxTreeWalk; i++ {
		n, ok := nodes[key(profile, cur)]
		if !ok || n.parent == "" {
			return stop, pause
		}
		pk := key(profile, n.parent)
		pn, known := nodes[pk]
		state := State(pn.state)
		if !known { // the parent is a root: it has no row with a parent of its own
			x, found, err := h.Store.GetSession(profile, n.parent)
			if err != nil {
				return false, false // cannot tell: touch nothing
			}
			state = State(x.State)
			if !found {
				return true, false // a session whose parent is gone does not outlive it
			}
		}
		if state == StateStopped {
			return true, pause
		}
		if state == StatePaused {
			pause = true
		}
		s, seen := stops[pk]
		if !seen {
			s = h.Store.latestStop(profile, n.parent, true).After(h.Store.turnFloor(profile, n.parent, ""))
			stops[pk] = s
		}
		if s {
			return true, pause
		}
		cur = n.parent
	}
	return stop, pause
}
