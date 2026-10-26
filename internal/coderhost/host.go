package coderhost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Turn is a running turn process.
type Turn interface {
	PID() int
	// Stamp says what makes PID this process and not a later one that reused
	// the number ("" when it cannot be told).
	Stamp() string
	// Wait blocks until the process ends.
	Wait() error
	// Stop asks the process (and its children) to end, then forces it.
	Stop()
}

// Launcher starts and finds turn processes. A turn is a `chat --conversation`
// process that journals itself; it must not die with the host.
type Launcher interface {
	Start(profileID, conversationID, turnID, message string, attachments []string, missionToken string) (Turn, error)
	// Adopt returns the turn process pid/stamp still names, or nil when it is gone.
	Adopt(pid int, stamp string) Turn
	// Finish closes a turn that ended without finishing itself. It is a no-op
	// for a turn that did.
	Finish(profileID, conversationID, turnID, reason string)
}

// Host carries commands out. Run it from one process only (the caller holds
// the single-instance lock).
type Host struct {
	Store *Store
	L     Launcher
	Poll  time.Duration
	Logf  func(format string, args ...any)

	// Mesh, when set, keeps the sessions visible on monomind's mesh and hands waiting
	// messages to the next turn (see Mesh). MeshEvery is how often it is brought up
	// to date (default 15 s); a session idle for MeshIdle (default 6 h) leaves.
	Mesh      Mesh
	MeshEvery time.Duration
	MeshIdle  time.Duration
	meshPrev  map[meshKey]MeshSession // what the mesh was last told, kept for a session whose records do not read

	// RequireConversations makes the host check that the conversation of a command still exists, and
	// sweep away sessions and commands of deleted ones. `coder host run` sets it; unit tests that
	// use sessions without conversation rows leave it off.
	RequireConversations bool

	// CheckinEvery is how often masters are looked at (default 30 s); the interval
	// each master asks for is its own setting.
	CheckinEvery time.Duration
	lastCheckin  time.Time

	mu   sync.Mutex // serializes session changes between the loop and the turn watchers
	live map[string]Turn
	wg   sync.WaitGroup

	readHolds map[int64]*readHold  // commands held for a failed read, Tick only
	capLogged map[string]time.Time // when each unreadable cap was last logged
	capHeld   map[string][2]string // chats whose sends the max_concurrent cap held on the last pass (profile, conversation); Tick only
}

func key(profileID, conversationID string) string { return profileID + "\x00" + conversationID }

func (h *Host) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// Recover reattaches to turn processes that outlived a previous host and closes
// the sessions whose process is gone.
func (h *Host) Recover() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.live == nil {
		h.live = map[string]Turn{}
	}
	sessions, err := h.Store.ListSessions("")
	if err != nil {
		return err
	}
	for _, x := range sessions {
		if x.TurnID == "" && x.TurnPID == 0 && x.State != StateRunning {
			continue
		}
		if t := h.L.Adopt(x.TurnPID, x.TurnStamp); t != nil && x.TurnPID > 0 {
			h.live[key(x.ProfileID, x.ConversationID)] = t
			h.watch(x, t)
			continue
		}
		if x.TurnID != "" {
			h.L.Finish(x.ProfileID, x.ConversationID, x.TurnID, "the coder host restarted and the turn's process was gone")
		}
		h.settle(x)
	}
	return nil
}

// settle clears a session's turn; a paused session stays paused. Callers hold mu.
func (h *Host) settle(x Session) {
	if cur, ok, err := h.Store.GetSession(x.ProfileID, x.ConversationID); err == nil && ok {
		x = cur
	}
	x.TurnID, x.TurnPID, x.TurnStamp = "", 0, ""
	if x.State != StatePaused {
		x.State = StateIdle
		if h.Store.OpenParked(x.ProfileID, x.ConversationID) > 0 {
			x.State = StateWaitingUser // a parked question outlives the turn that was running when it was parked
		}
	}
	if err := h.Store.SaveSession(x); err != nil {
		h.logf("coder host: saving %s: %v", x.ConversationID, err)
	}
}

// watch waits for the turn to end in the background.
func (h *Host) watch(x Session, t Turn) {
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		_ = t.Wait()
		h.L.Finish(x.ProfileID, x.ConversationID, x.TurnID, "the turn's process ended without finishing the turn")
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.live, key(x.ProfileID, x.ConversationID))
		h.settle(x)
	}()
}

// Tick makes one pass over the waiting commands. A command that cannot run yet
// (a send to a busy or paused session) stays pending, in order.
func (h *Host) Tick() error {
	h.Store.ReopenFailedAnswers() // an answer note that did not run (stop, expiry, a failed check) gives its question back
	h.Store.DropUnstartedCaps()
	if err := h.Store.ExpireSends(); err != nil {
		return err
	}
	cmds, err := h.pendingOrdered()
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	heldNow := map[string][2]string{}
	defer func() { h.capHeld = heldNow }()
	if h.live == nil {
		h.live = map[string]Turn{}
	}
	blocked := map[string]bool{} // an earlier send for the chat is still waiting: later sends keep their order
	var running *childRunning    // built on the first send to a child (max_concurrent)
	for _, c := range cmds {
		k := key(c.ProfileID, c.ConversationID)
		// cmds was read before the pass began: an earlier command of this pass (a stop, which fails the
		// chat's earlier sends, or a master's stop) may have ended this one. Act only on a command that is
		// still pending, and let an unreadable row wait for the next pass.
		if cur, ok, err := h.Store.GetCommand(c.ID); err != nil || !ok || cur.Status != CmdPending {
			continue
		}
		if h.RequireConversations {
			// A command read just before its conversation was deleted must not bring the session
			// back, nor start a turn on nothing.
			if ok, err := h.Store.ConversationExists(c.ProfileID, c.ConversationID); err != nil {
				return err
			} else if !ok {
				h.end(c, CmdFailed, "the conversation was deleted")
				continue
			}
		}
		x, err := h.Store.EnsureSession(c.ProfileID, c.ConversationID)
		if err != nil {
			return err
		}
		// A command a master queued is run only while the target is still that master's descendant and
		// the master is alive; a paused master's sends are held (they stay pending, in order, and do not
		// hold up a person's message to the same chat).
		if c.Issuer != "" {
			if ok, hold, reason := h.issuerStillAllowed(c, x); !ok {
				if strings.HasPrefix(reason, readFailed) {
					h.recheckFailed(c, reason, k, blocked) // a failed read holds the command; it fails only after repeated passes
				} else if !hold {
					h.end(c, CmdFailed, reason)
				}
				continue
			}
		}
		switch c.Kind {
		case KindSend:
			if blocked[k] || h.live[k] != nil || x.State == StatePaused {
				blocked[k] = true
				continue
			}
			if x.State == StateStopped {
				h.end(c, CmdFailed, "the session is stopped")
				continue
			}
			// A check-in is queued by the host, not typed by a person: it is only run for a
			// master that is still unattended, as it was when it was queued.
			if strings.HasPrefix(c.TurnID, CheckinTurnPrefix) {
				if reason := h.checkinStillAllowed(x, h.Store.now()); reason != "" {
					h.recheckFailed(c, reason, k, blocked)
					continue
				}
			}
			// A message a master sent to a child counts with the check-ins: after CheckinLimit
			// host-made turns in a row the child waits for a person, and it is only run for a
			// session that is still a child.
			if strings.HasPrefix(c.TurnID, MasterTurnPrefix) || c.Issuer != "" {
				if reason := h.masterSendStillAllowed(x); reason != "" {
					h.recheckFailed(c, reason, k, blocked)
					continue
				}
			}
			// An answer note from a master or an applied default counts with the check-ins too: a
			// person's own answer is not held back.
			if answerGated(c.TurnID) {
				if reason := h.answerStillAllowed(x); reason != "" {
					h.recheckFailed(c, reason, k, blocked)
					continue
				}
			}
			// max_concurrent: a master's children run only so many turns at once; the rest wait, in order.
			if x.Role == "child" && x.Parent != "" {
				if running == nil {
					running = h.runningChildren(cmds)
				}
				if h.childSlotFull(x, k, running) {
					heldNow[k] = [2]string{c.ProfileID, c.ConversationID}
					blocked[k] = true
					continue
				}
			}
			// A send the person asked to run as a mission starts only with its genuine, live
			// confirmation: without one it is not run as a plain turn (that would silently
			// drop what was asked for). The record is written just after the command is
			// queued, so a missing one is waited for a minute before it counts as lost.
			token := ""
			if c.WantsMission {
				if mc, ok := h.Store.PeekConfirm(c); ok {
					token = mc.Token()
				} else if t, err := time.Parse(timeLayout, c.CreatedAt); err == nil && h.Store.now().Sub(t) < MissionConfirmGrace {
					blocked[k] = true
					continue
				} else {
					h.end(c, CmdFailed, "mission confirmation expired or missing: send again")
					continue
				}
			}
			// The turn is recorded before it starts, without a pid yet, so the
			// app's startup sweep sees it as the host's while it is admitted.
			x.State, x.TurnID, x.TurnPID, x.TurnStamp = StateRunning, c.TurnID, 0, ""
			if err := h.Store.SaveSession(x); err != nil {
				h.end(c, CmdFailed, "could not record the session: "+err.Error())
				return err
			}
			message, commit := c.Message, func() {}
			if h.Mesh != nil {
				if ms, ok := h.meshSessionFor(c.ProfileID, c.ConversationID); ok {
					block, done := h.Mesh.Inbox(ms)
					switch {
					case block == "":
						done() // what was looked at was all out of scope: consume it, or it hides what comes after
					case len(block)+2+len(message) <= MaxMessageBytes:
						// Peer content goes ahead of the person's message, sealed as data, but never
						// at the cost of the message: a block that would not fit stays unread.
						message, commit = block+"\n\n"+message, done
					}
				}
			}
			capUSD, capped, berr := h.Store.TurnCap(c.ProfileID, c.ConversationID)
			if berr == nil && capped {
				if _, ok := h.L.(BudgetLauncher); !ok {
					berr = errors.New("this launcher cannot start a turn under a spend cap")
				}
			}
			if berr != nil {
				h.settle(x)
				h.end(c, CmdFailed, berr.Error())
				continue
			}
			// The turn's cap is recorded BEFORE the turn starts (and dropped if it does not), so a crash or a
			// failed write can never leave a running turn with no cap on record.
			if capped {
				if err := h.Store.RecordTurnCap(c.ProfileID, c.ConversationID, c.TurnID, capUSD); err != nil {
					h.settle(x)
					h.end(c, CmdFailed, "could not record the turn's spend cap: "+err.Error())
					continue
				}
			}
			t, err := h.unlocked(func() (Turn, error) { // admission can take seconds; do not hold up the turn watchers meanwhile
				// The token is the one of the confirmation record looked up above (never anything
				// in the command row, the message or the mesh block); the child verifies the
				// record's MAC and consumes it.
				if capped {
					return h.L.(BudgetLauncher).StartCapped(c.ProfileID, c.ConversationID, c.TurnID, message, c.Attachments, token, capUSD)
				}
				return h.L.Start(c.ProfileID, c.ConversationID, c.TurnID, message, c.Attachments, token)
			})
			if err != nil {
				if capped {
					h.Store.DropTurnCap(c.ProfileID, c.ConversationID, c.TurnID)
				}
				h.settle(x)
				h.end(c, CmdFailed, err.Error())
				continue
			}
			if capped {
				h.Store.MarkTurnCapStarted(c.ProfileID, c.ConversationID, c.TurnID)
			}
			if c.Issuer != "" && !HostMade(c.TurnID) {
				h.Store.MarkHostTurn(c.ProfileID, c.ConversationID, c.TurnID) // counts like a master- turn
			}
			x.TurnPID, x.TurnStamp = t.PID(), t.Stamp()
			if err := h.Store.SaveSession(x); err != nil {
				t.Stop()
				h.end(c, CmdFailed, startedMarker+" but could not record the session: "+err.Error())
				return err
			}
			_, _ = h.unlocked(func() (Turn, error) { commit(); return nil, nil }) // the turn has the messages and is recorded: mark them read, without holding up the watchers
			h.live[k] = t
			if running != nil {
				running.started(x, k)
			}
			h.watch(x, t)
			h.end(c, CmdDone, "")
		case KindStop:
			// A stop also drops the sends queued before it: the operator
			// stopped the session, not just the turn that happened to run.
			dropped, err := h.Store.CancelPendingSends(c.ProfileID, c.ConversationID, c.ID, "cancelled by a stop")
			if err != nil {
				return err
			}
			// The master was stopped: what it had queued for its children is not run either.
			if err := h.Store.CancelChildMasterSends(c.ProfileID, c.ConversationID, "the master was stopped"); err != nil {
				return err
			}
			t := h.live[k]
			if t == nil && dropped == 0 {
				h.end(c, CmdFailed, "no turn is running and nothing is queued")
				continue
			}
			if t != nil {
				t.Stop()
			}
			h.end(c, CmdDone, "")
		case KindPause:
			x.State = StatePaused
			if err := h.Store.SaveSession(x); err != nil {
				return err
			}
			h.end(c, CmdDone, "")
		case KindResume:
			if x.State == StatePaused {
				x.State = StateIdle
				if h.Store.OpenParked(x.ProfileID, x.ConversationID) > 0 {
					x.State = StateWaitingUser // a parked question outlives a pause
				}
				if h.live[k] != nil {
					x.State = StateRunning
				}
				if err := h.Store.SaveSession(x); err != nil {
					return err
				}
			}
			h.end(c, CmdDone, "")
		default:
			h.end(c, CmdFailed, fmt.Sprintf("unknown command %q", c.Kind))
		}
	}
	return nil
}

// endBetweenSteps runs between end's two steps; a test places a confirmation there.
var endBetweenSteps = func() {}

func (h *Host) end(c Command, status, errText string) {
	// The status moves first and the record is revoked after: a confirmation written
	// while this runs either sees the command no longer pending and refuses, or is in
	// place before the revoke.
	// Only a command that is still pending is ended: one that another process already ended (an
	// operator's stop) keeps the status and reason it was given.
	if applied, err := h.Store.setCommandIfPending(c.ID, status, errText); err != nil {
		h.logf("coder host: recording command %d: %v", c.ID, err)
	} else if !applied {
		h.logf("coder host: command %d was ended elsewhere; its status is left as it is", c.ID)
	}
	endBetweenSteps()
	if status == CmdFailed && c.Kind == KindSend {
		h.Store.Revoke(c.ProfileID, c.ConversationID, c.TurnID, c.ID) // a send that did not start leaves no live confirmation
	}
}

// Run recovers, then ticks until ctx ends. Turn processes keep running when it
// returns: that is the point of the host.
func (h *Host) Run(ctx context.Context) error {
	if err := h.Recover(); err != nil {
		return err
	}
	poll := h.Poll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	if h.Mesh != nil {
		meshDone := make(chan struct{})
		go func() {
			defer close(meshDone)
			h.meshLoop(ctx)
		}()
		defer func() { <-meshDone; h.Mesh.Close() }()
	}
	tk := time.NewTicker(poll)
	defer tk.Stop()
	for {
		if err := h.Tick(); err != nil {
			h.logf("coder host: %v", err)
		}
		h.checkinIfDue()
		select {
		case <-ctx.Done():
			return nil
		case <-tk.C:
		}
	}
}

// Wait blocks until every turn watcher has finished (tests).
func (h *Host) Wait() { h.wg.Wait() }

// meshLoop brings the mesh up to date now and every MeshEvery until ctx ends.
func (h *Host) meshLoop(ctx context.Context) {
	every := h.MeshEvery
	if every <= 0 {
		every = 15 * time.Second
	}
	for {
		h.meshSync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// meshSync tells the mesh which sessions to show: those whose settings make them
// visible and that are not stopped, are doing something, or were touched within
// MeshIdle. What it says about a session is generic (its state word), never anything
// from its prompts, its folder's path or its access level.
func (h *Host) meshSync(ctx context.Context) {
	idle := h.MeshIdle
	if idle <= 0 {
		idle = 6 * time.Hour
	}
	sessions, err := h.Store.ListSessions("")
	if err != nil {
		h.logf("coder host: mesh: %v", err)
		return
	}
	children := map[string][]string{}
	for _, x := range sessions {
		if x.Parent != "" {
			children[x.Parent] = append(children[x.Parent], x.ConversationID)
		}
	}
	var want []MeshSession
	next := map[meshKey]MeshSession{}
	for _, x := range sessions {
		active := x.State == StateRunning || x.State == StateWaitingUser || x.State == StatePaused
		if x.State == StateStopped {
			continue
		}
		if t, err := time.Parse(timeLayout, x.UpdatedAt); !active && (err != nil || h.Store.now().Sub(t) > idle) {
			continue
		}
		key := meshKey{x.ProfileID, x.ConversationID}
		settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
		if err != nil {
			// A settings row that does not read must not freeze the others, and must not make
			// this session leave and come back under a new address: it stays as it was.
			h.logf("coder host: mesh: %s: %v", x.ConversationID, err)
			if prev, ok := h.meshPrev[key]; ok {
				want, next[key] = append(want, prev), prev
			}
			continue
		}
		if !settings.MeshVisible {
			continue
		}
		cwd, _, ok, err := h.Store.ConversationInfo(x.ProfileID, x.ConversationID)
		if err != nil {
			if prev, ok := h.meshPrev[key]; ok {
				want, next[key] = append(want, prev), prev
			}
			continue
		}
		if !ok || cwd == "" {
			continue
		}
		started, _ := time.Parse(timeLayout, x.CreatedAt)
		related := append([]string{}, children[x.ConversationID]...)
		if x.Parent != "" {
			related = append(related, x.Parent)
		}
		ms := MeshSession{
			ProfileID: x.ProfileID, ConversationID: x.ConversationID, Cwd: cwd, Started: started.Local(),
			Task: string(x.State), Alias: settings.MeshAlias, // nothing about its work, its folder's path or its access level
			Accept: settings.MeshAccept, Related: related,
		}
		next[key] = ms
		want = append(want, ms)
	}
	h.meshPrev = next
	h.Mesh.Sync(ctx, want)
}

// meshSessionFor is the mesh's view of one session as it is right now, for reading
// its inbox with the scope it has now and not the one of the last pass.
func (h *Host) meshSessionFor(profileID, conversationID string) (MeshSession, bool) {
	settings, err := h.Store.GetSettings(profileID, conversationID)
	if err != nil || !settings.MeshVisible || settings.MeshAccept == "none" {
		return MeshSession{}, false
	}
	related := []string{}
	if sessions, err := h.Store.ListSessions(profileID); err == nil {
		for _, x := range sessions {
			switch {
			case x.Parent == conversationID && x.State != StateStopped:
				related = append(related, x.ConversationID)
			case x.ConversationID == conversationID && x.Parent != "":
				if p, ok, err := h.Store.GetSession(profileID, x.Parent); err == nil && ok && p.State != StateStopped {
					related = append(related, x.Parent)
				}
			}
		}
	}
	return MeshSession{ProfileID: profileID, ConversationID: conversationID, Accept: settings.MeshAccept, Related: related}, true
}

// unlocked runs f without h.mu (which the caller holds) and takes it back, also if f panics.
func (h *Host) unlocked(f func() (Turn, error)) (Turn, error) {
	h.mu.Unlock()
	defer h.mu.Lock()
	return f()
}

// checkinIfDue runs a check-in pass when CheckinEvery has passed since the last.
func (h *Host) checkinIfDue() {
	if h.RequireConversations {
		if n, err := h.Store.SweepOrphans(); err != nil {
			h.logf("coder host: sweeping sessions of deleted conversations: %v", err)
		} else if n > 0 {
			h.logf("coder host: removed %d row(s) of deleted conversations", n)
		}
	}
	every := h.CheckinEvery
	if every <= 0 {
		every = 30 * time.Second
	}
	if now := h.Store.now(); now.Sub(h.lastCheckin) >= every {
		h.lastCheckin = now
		h.checkinPass(now)
		h.parkedPass()
	}
}

// checkinStillAllowed re-checks, when a host-made check-in is about to run, what
// allowed it to be queued: it can have waited, and the settings can have changed.
// It returns why not, or "".
func (h *Host) checkinStillAllowed(x Session, now time.Time) string {
	kids, err := h.liveChildren(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's children do not read: " + err.Error()
	}
	if kids == 0 && x.Unattended && x.Role != "child" { // a lead is checked on while its mission has open work: a failed read is not "no mission"
		if _, ok := h.missionOf(x.ProfileID, x.ConversationID); !ok {
			return readFailed + "the session's mission does not read"
		}
	}
	if reason := h.checkinTargetStillValid(x, kids); reason != "" {
		return reason
	}
	settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's settings do not read: " + err.Error()
	}
	if !withinActiveHours(settings.ActiveHours, now) {
		return "outside the session's active hours"
	}
	streak, _, _, err := h.Store.CheckinStreak(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's turns do not read: " + err.Error()
	}
	if streak >= settings.CheckinLimit {
		return "the session has had its check-ins in a row; it waits for a person"
	}
	return ""
}

// answerStillAllowed re-checks, when an answer note from a master or a default is
// about to run, that the session has not had CheckinLimit host-made turns in a row.
func (h *Host) answerStillAllowed(x Session) string {
	settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's settings do not read: " + err.Error()
	}
	streak, _, _, err := h.Store.CheckinStreak(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's turns do not read: " + err.Error()
	}
	if streak >= settings.CheckinLimit {
		return "the session has had its host-made turns in a row; it waits for a person"
	}
	return ""
}

// masterSendStillAllowed re-checks, when a master's message to a child is about to
// run, that the session is still a child of a master that has not been stopped
// and has not had CheckinLimit host-made turns in a row without a person. It
// returns why not, or "".
func (h *Host) masterSendStillAllowed(x Session) string {
	if x.Role != "child" || x.Parent == "" {
		return "the session is no longer a child session"
	}
	settings, err := h.Store.GetSettings(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's settings do not read: " + err.Error()
	}
	streak, _, _, err := h.Store.CheckinStreak(x.ProfileID, x.ConversationID)
	if err != nil {
		return readFailed + "the session's turns do not read: " + err.Error()
	}
	if streak >= settings.CheckinLimit {
		return "the session has had its host-made turns in a row; it waits for a person"
	}
	return ""
}

// issuerStillAllowed is the check every command a master queued (c.Issuer != "") passes when it is about
// to run: the target x must still be a descendant of the issuer (the predicate the tools authorize with),
// and the issuer must still exist and not be stopped. A send from a paused master is held (hold is true:
// leave it pending, in order, like a send to a paused chat); a stop, pause or resume from a paused master
// still runs, so a master can always stop a runaway child. reason says why not when ok is false and hold
// is false. A command with no issuer is always allowed here.
func (h *Host) issuerStillAllowed(c Command, x Session) (ok bool, hold bool, reason string) {
	if c.Issuer == "" {
		return true, false, ""
	}
	below, err := h.Store.IsDescendantErr(c.ProfileID, c.Issuer, c.ConversationID)
	if err != nil {
		return false, true, readFailed + "the session's place in the tree does not read: " + err.Error()
	}
	if !below {
		return false, false, "the session is no longer a child of the master that sent this"
	}
	m, found, err := h.Store.GetSession(c.ProfileID, c.Issuer)
	if err != nil {
		return false, true, readFailed + "the session's master does not read: " + err.Error()
	}
	if !found || m.State == StateStopped {
		return false, false, "the session's master is gone or stopped"
	}
	if m.State == StatePaused && c.Kind == KindSend {
		return false, true, "the session's master is paused"
	}
	return true, false, ""
}
