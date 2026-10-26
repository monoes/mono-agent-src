package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// ─── Coder turns run by the coder host ──────────────────────────────────────
//
// A coder conversation's turn is not a child of this app: `coder sessions send`
// queues it and the coder host (`coder host`) runs it as a detached process, so
// it keeps going when the app closes. The app only sends commands and follows
// the turn's journal, emitting each committed event the way relayTurn does for
// a turn it started itself. Scoped chats keep the old path (startTurn).

// hostOwnerPrefix starts the owner instance id of every turn the host runs.
const hostOwnerPrefix = "host-"

func isHostOwner(owner string) bool { return strings.HasPrefix(owner, hostOwnerPrefix) }

// hostRelayPoll is how often a followed turn's journal is read (each read is a
// CLI call); it backs off to hostRelayIdlePoll when nothing new arrives.
var (
	hostRelayPoll      = time.Second
	hostRelayIdlePoll  = 3 * time.Second
	hostRelayIdleAfter = 30 * time.Second
)

// isCoderConversation says whether the conversation is above the scoped level.
// A conversation it cannot read is not routed to the host.
func (a *App) isCoderConversation(profileID, conversationID string) bool {
	var rec ai.ConversationRecord
	if err := a.chatSup.cli(profileID, &rec, "chat", "history", "show", conversationID); err != nil {
		return false
	}
	return !rec.Conversation().Scoped()
}

// detach stops following the turn without stopping it.
func (h *chatTurnHandle) detach() {
	h.mu.Lock()
	cancel := h.cancel
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// startHostTurn queues the turn with the host and follows its journal.
func (sup *chatSupervisor) startHostTurn(h *chatTurnHandle, message string, attachments []string) (string, error) {
	args := []string{"coder", "sessions", "send", "--turn", h.turnID}
	for _, p := range attachments {
		args = append(args, "--attach="+p)
	}
	if h.mission {
		// Before the "--": the confirmation is the send command's own flag, not text in the message.
		args = append(args, "--mission")
	}
	args = append(args, "--", h.conversationID, message)
	var res struct {
		CommandID int64 `json:"commandId"`
	}
	if err := sup.cli(h.profileID, &res, args...); err != nil {
		return "", err
	}
	h.host = true
	sup.follow(h, res.CommandID, 0)
	return fmt.Sprintf(`{"ok":true,"turnId":%q,"status":"active"}`, h.turnID), nil
}

// followHostTurn follows a host turn this app did not just send (one found
// active when a conversation is opened), unless it already does.
func (sup *chatSupervisor) followHostTurn(profileID, conversationID, turnID string, afterSeq int64) {
	if profileID == "" {
		return
	}
	h, already, err := sup.admit(conversationID, turnID)
	if err != nil || already {
		return // another turn is registered for the chat, or this one is followed
	}
	h.profileID, h.host = profileID, true
	sup.follow(h, 0, afterSeq)
}

func (sup *chatSupervisor) follow(h *chatTurnHandle, commandID, afterSeq int64) {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	h.mu.Unlock()
	sup.running.Add(1)
	go func() {
		defer sup.running.Done()
		defer cancel()
		sup.relayHost(ctx, h, commandID, afterSeq)
	}()
}

// relayHost emits each committed event of the turn, in order, until it
// finishes. A send that fails before its turn exists (the host refused it)
// ends with a live-only turn.finished carrying the reason.
func (sup *chatSupervisor) relayHost(ctx context.Context, h *chatTurnHandle, commandID, seq int64) {
	defer func() {
		select {
		case <-ctx.Done(): // detached at shutdown: the slot dies with the process
		default:
			sup.release(h.conversationID, h.turnID)
		}
	}()
	last := time.Now()
	for {
		var page chatEventPage
		err := sup.cli(h.profileID, &page, "chat", "history", "events", h.conversationID, h.turnID,
			"--after-seq", strconv.FormatInt(seq, 10), "--limit", "200")
		switch {
		case err == nil:
			for _, r := range page.Items {
				ev := r.Event()
				if r.Seq > seq {
					seq = r.Seq
				}
				last = time.Now()
				if sup.emit != nil {
					sup.emit(ev)
				}
				if r.Type == chatevents.EventTurnFinished {
					return
				}
			}
			if page.HasMore {
				continue
			}
		case chatCLIExitCode(err) == 2 && commandID > 0: // no such turn yet: did the send fail?
			if reason, failed := sup.hostCommandFailed(h.profileID, commandID); failed {
				sup.emitLiveFinish(h, reason)
				return
			}
		}
		wait := hostRelayPoll
		if time.Since(last) > hostRelayIdleAfter {
			wait = hostRelayIdlePoll
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// hostCommandFailed reports whether the host ended the command as failed, and why.
func (sup *chatSupervisor) hostCommandFailed(profileID string, commandID int64) (string, bool) {
	var c struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := sup.cli(profileID, &c, "coder", "sessions", "command", strconv.FormatInt(commandID, 10)); err != nil || c.Status != "failed" {
		return "", false
	}
	if c.Error == "" {
		c.Error = "the coder host could not start the turn"
	}
	return c.Error, true
}

// emitLiveFinish ends the turn in the UI when no journal will (seq MaxSafeSeq
// so the frontend never drops it as stale, as finishOrphanedTurn does).
func (sup *chatSupervisor) emitLiveFinish(h *chatTurnHandle, reason string) {
	ev, err := chatevents.New(h.profileID, h.conversationID, h.turnID, chatevents.MaxSafeSeq, time.Now(), chatevents.EventTurnFinished,
		chatevents.TurnFinishedPayload{Status: chatevents.StatusFailed, Reason: reason, HistorySaved: false})
	if err == nil && sup.emit != nil {
		sup.emit(ev)
	}
}

// stopHostTurn asks the host to stop the conversation's running turn.
func (a *App) stopHostTurn(conversationID string) string {
	pid, err := a.profileID()
	if err != nil {
		return a.chatBindingError(err)
	}
	if err := a.chatSup.cli(pid, nil, "coder", "sessions", "stop", conversationID); err != nil {
		return a.chatBindingError(err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true})
	return string(b)
}
