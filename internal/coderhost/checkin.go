package coderhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// The check-in scheduler. It is deterministic code, not a model that sleeps: at
// each pass it looks at a master's children and, when the settings say it is
// time (or something happened), queues one short turn for the master with a
// digest of what it saw. Everything in the digest comes from the host's own
// records (ids, state words, turn status words, ages); no text an agent wrote
// reaches it, so a child cannot steer its master through its own status.

// Turns the host makes by itself carry the id prefix "checkin-"; CheckinStreak counts them. A later path that
// starts turns without a person (a wake-up from a message, a schedule) must use its own recognizable prefix and
// be counted the same way, or it would reset the limit.
//
// MinCheckinGap is the shortest time between two check-ins of one master, event
// triggers included.
const MinCheckinGap = time.Minute

// childFact is what is known of one child at a pass.
type childFact struct {
	ID       string `json:"id"`
	State    State  `json:"state"`
	Queued   int    `json:"queued"`
	LastTurn string `json:"last_turn"`   // id of its newest turn
	Status   string `json:"last_status"` // its status: active, completed, failed, cancelled, interrupted
	Silent   bool   `json:"silent"`      // running with no journal event for silent_minutes
	SilentMi int    `json:"-"`
}

type snapshot struct {
	Children []childFact `json:"children"`
}

// checkinPass looks at every master once. It never runs a turn: it queues a send.
func (h *Host) checkinPass(now time.Time) {
	sessions, err := h.Store.ListSessions("")
	if err != nil {
		h.logf("coder host: check-in: %v", err)
		return
	}
	kids := map[string][]Session{}
	for _, x := range sessions {
		if x.Parent != "" && x.State != StateStopped && hostID.MatchString(x.ConversationID) {
			kids[x.ProfileID+"\x00"+x.Parent] = append(kids[x.ProfileID+"\x00"+x.Parent], x)
		}
	}
	for _, m := range sessions {
		children := kids[m.ProfileID+"\x00"+m.ConversationID]
		if m.Role != "master" || len(children) == 0 {
			continue
		}
		h.checkinOne(now, m, children)
	}
}

func (h *Host) checkinOne(now time.Time, m Session, children []Session) {
	// The session has to be free: a turn running, a pause, a command queued all mean
	// the master is being dealt with already. Nothing is recorded, so it is looked at
	// again on the next pass (one catch-up, never a pile).
	if !m.Unattended || m.State != StateIdle || m.TurnID != "" || h.Store.PendingFor(m.ProfileID, m.ConversationID) > 0 {
		return
	}
	settings, err := h.Store.GetSettings(m.ProfileID, m.ConversationID)
	if err != nil {
		h.logf("coder host: check-in for %s: settings: %v", m.ConversationID, err)
		return
	}
	prev, err := h.Store.GetCheckin(m.ProfileID, m.ConversationID)
	if err != nil {
		h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
		return
	}
	cur := h.snapshotOf(now, m.ProfileID, children, settings.SilentMinutes)
	curJSON, _ := json.Marshal(cur)
	nowStr := now.UTC().Format(timeLayout)
	remember := func() error {
		return h.Store.SaveCheckin(m.ProfileID, m.ConversationID, Checkin{At: nowStr, Snapshot: string(curJSON)})
	}

	last, _ := time.Parse(timeLayout, prev.At)
	if prev.At == "" || now.Before(last) {
		// First sight of this master, or the clock went back past the last look:
		// remember the children as they are and check in after one interval.
		if err := remember(); err != nil {
			h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
		}
		return
	}
	// Spacing is measured from the end of the master's own last turn as well as from the
	// last check-in, so a long turn is not followed at once by another.
	streak, lastTurnEnd, lastFailed, err := h.Store.CheckinStreak(m.ProfileID, m.ConversationID)
	if err != nil {
		h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
		return // the limit cannot be checked, so nothing is queued
	}
	if now.Sub(last) < MinCheckinGap || (!lastTurnEnd.IsZero() && now.Sub(lastTurnEnd) < MinCheckinGap) || !withinActiveHours(settings.ActiveHours, now) {
		return
	}
	// Enough check-ins in a row with nobody having written to this master: it waits for a person.
	if streak >= settings.CheckinLimit {
		return
	}
	var before snapshot
	_ = json.Unmarshal([]byte(prev.Snapshot), &before)
	events := triggeredEvents(before, cur, settings.CheckinTriggers)
	if lastFailed {
		events = nil // after a check-in turn that did not complete, only the timer, not events, brings the next
	}
	interval := time.Duration(settings.CheckinMinutes) * time.Minute
	due := settings.CheckinMinutes > 0 && now.Sub(last) >= interval && (!lastFailed || now.Sub(lastTurnEnd) >= interval)
	changed := string(curJSON) != prev.Snapshot

	switch {
	case len(events) > 0:
	case due && (!settings.SkipIfUnchanged || changed):
	case due && !lastFailed:
		// Nothing changed and the settings say skip: this counts as a look.
		if err := remember(); err != nil {
			h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
		}
		return
	default:
		return
	}
	// The master may have been given something to do since it was read at the top of the
	// pass; the view is read again so a check-in does not start behind a person's message.
	if fresh, ok, err := h.Store.GetSession(m.ProfileID, m.ConversationID); err != nil || !ok || !fresh.Unattended || fresh.Role != "master" ||
		fresh.State != StateIdle || fresh.TurnID != "" || h.Store.PendingFor(m.ProfileID, m.ConversationID) > 0 {
		return
	}
	reason := "scheduled"
	if len(events) > 0 {
		reason = strings.Join(events, ", ")
	}
	// Recorded first: if the record cannot be written, nothing is queued, so a write that
	// keeps failing cannot turn into a check-in every pass.
	if err := remember(); err != nil {
		h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
		return
	}
	turn := make([]byte, 8)
	_, _ = rand.Read(turn)
	if _, err := h.Store.Enqueue(Command{
		ProfileID: m.ProfileID, ConversationID: m.ConversationID, Kind: KindSend,
		TurnID: "checkin-" + hex.EncodeToString(turn), Message: digestText(reason, cur),
	}); err != nil {
		h.logf("coder host: check-in for %s: %v", m.ConversationID, err)
	}
}

func (h *Host) snapshotOf(now time.Time, profileID string, children []Session, silentMinutes int) snapshot {
	var out snapshot
	for _, c := range children {
		if !hostID.MatchString(c.ConversationID) {
			continue // an id that would not be safe in the master's prompt is left out of it
		}
		id, status := h.Store.LastTurn(profileID, c.ConversationID)
		f := childFact{ID: c.ConversationID, State: c.State, Queued: h.Store.PendingFor(profileID, c.ConversationID), LastTurn: id, Status: status}
		if c.State == StateRunning && c.TurnID != "" {
			// Quiet since the newest event of the turn that is running now (or since it began).
			if since := h.Store.TurnActivity(profileID, c.ConversationID, c.TurnID); !since.IsZero() {
				f.SilentMi = int(now.Sub(since) / time.Minute)
				f.Silent = f.SilentMi >= silentMinutes
			}
		}
		out.Children = append(out.Children, f)
	}
	sort.Slice(out.Children, func(i, j int) bool { return out.Children[i].ID < out.Children[j].ID })
	return out
}

// hostID is what a conversation id must look like to be put in a prompt.
var hostID = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// triggeredEvents names the events that happened between two snapshots and that
// the settings ask to check in on. A turn counts when it newly reached an end
// state: a new turn that ended, or the one that was running at the last snapshot
// and has ended since. child_question and budget_80 are settings too, but nothing
// records a question or a spend yet, so they never fire; likewise a child is never
// "waiting for the user" yet, so child_blocked fires for a pause only.
func triggeredEvents(before, now snapshot, enabled []string) []string {
	on := map[string]bool{}
	for _, e := range enabled {
		on[e] = true
	}
	old := map[string]childFact{}
	for _, c := range before.Children {
		old[c.ID] = c
	}
	ended := func(status string) bool {
		return status == "completed" || status == "failed" || status == "interrupted"
	}
	found := map[string]bool{}
	for _, c := range now.Children {
		o, known := old[c.ID]
		if !known {
			continue
		}
		newlyEnded := ended(c.Status) && (c.LastTurn != o.LastTurn || !ended(o.Status))
		switch {
		case newlyEnded && c.Status == "completed":
			found["child_finished"] = true
		case newlyEnded:
			found["child_failed"] = true
		}
		if (c.State == StateWaitingUser || c.State == StatePaused) && o.State != c.State {
			found["child_blocked"] = true
		}
		if c.Silent && !o.Silent {
			found["child_silent"] = true
		}
	}
	var out []string
	for e := range found {
		if on[e] {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// digestText is the master's prompt for a check-in: facts only.
func digestText(reason string, s snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[CHECK-IN] Progress check (%s). The sessions you coordinate, as the host sees them now:\n", reason)
	for _, c := range s.Children {
		last := "no turn yet"
		switch c.Status {
		case "active", "completed", "failed", "cancelled", "interrupted":
			last = c.Status
		case "":
		default:
			last = "unknown"
		}
		fmt.Fprintf(&b, "- %s: %s; last turn %s; %d message(s) queued", c.ID, c.State, last, c.Queued)
		if c.Silent {
			fmt.Fprintf(&b, "; no activity for %d min", c.SilentMi)
		}
		b.WriteString("\n")
	}
	b.WriteString("Review them and give each the direction it needs. Do not redo finished work. This message was written by the host, not by the person or by any of these sessions.")
	return b.String()
}

// withinActiveHours says whether now falls in "HH:MM-HH:MM" (local time; a window
// that crosses midnight works); "" is always.
func withinActiveHours(window string, now time.Time) bool {
	if window == "" {
		return true
	}
	var sh, sm, eh, em int
	if _, err := fmt.Sscanf(window, "%d:%d-%d:%d", &sh, &sm, &eh, &em); err != nil {
		return false // an unreadable window (not written through the settings) means no unattended turns
	}
	local := now.Local()
	cur := local.Hour()*60 + local.Minute()
	start, end := sh*60+sm, eh*60+em
	if start <= end {
		return cur >= start && cur < end
	}
	return cur >= start || cur < end
}
