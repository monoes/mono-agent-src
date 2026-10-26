package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// ─── Wails bindings ─────────────────────────────────────────────────────────
//
// Contracts per the plan (§175), unchanged by the move to the CLI: the
// CLI's snake_case records are converted back to the ai/chatevents types
// whose JSON these bindings always returned. Responses follow the existing
// JSON-string convention: {"error":...} on failure, otherwise a typed
// payload.

func (a *App) chatBindingError(err error) string { return aiError(err) }

// isForeignActiveTurn reports whether t is an active turn owned by a
// DIFFERENT, identified live instance than instanceID — the one case in
// which another app instance's turn (sharing this database) is read-only
// here: it drives GetChatTurns' ownedByThisInstance and StopChatTurn's
// explicit foreign-turn failure. A terminal turn is never foreign, and
// neither is an active turn with no recorded owner (a row predating
// ownership), which falls back to "treat as mine".
func isForeignActiveTurn(t ai.Turn, instanceID string) bool {
	return t.Status == "active" && t.OwnerInstanceID != "" && t.OwnerInstanceID != instanceID && !isHostOwner(t.OwnerInstanceID)
}

// chatTurnListItem is one GetChatTurns response entry: the stored turn plus
// a derived, instance-scoped ownership flag. ai.Turn.OwnerInstanceID stays
// json:"-" (never exposed raw).
type chatTurnListItem struct {
	ai.Turn
	OwnedByThisInstance bool `json:"ownedByThisInstance"`
}

// CreateChatConversation creates a new scoped agent conversation.
// workflowID is the tool/ownership context ("general"/"draft"/an owned
// workflow id).
func (a *App) CreateChatConversation(workflowID, runtimeID, model, effort string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return a.chatBindingError(fmt.Errorf("chat supervisor not initialized"))
	}
	args := []string{"chat", "history", "create", "--runtime", runtimeID, "--workflow", workflowID}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	var rec ai.ConversationRecord
	if err := a.chatCLI(&rec, args...); err != nil {
		return a.chatBindingError(err)
	}
	b, _ := json.Marshal(rec.Conversation())
	return string(b)
}

// StartChatTurn admits and starts one turn. Duplicate calls with the same
// turnID against an already-admitted turn return the existing admission
// (idempotent Start); a different turnID while one is active returns busy.
// Every response carries turnId (plan §189). attachments are files for a
// coder turn (SaveChatAttachment's paths, or any file the user picked),
// passed to the CLI as --attach.
func (a *App) StartChatTurn(conversationID, turnID, message string, tools, allowRuns bool, attachments []string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return a.chatBindingError(fmt.Errorf("chat supervisor not initialized"))
	}
	pid, err := a.profileID()
	if err != nil {
		return a.chatBindingError(err)
	}
	h, alreadyActive, err := a.chatSup.admit(conversationID, turnID)
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"turnId":%q,"status":"busy"}`, turnID)
	}
	if alreadyActive {
		return fmt.Sprintf(`{"ok":true,"turnId":%q,"status":"active"}`, turnID)
	}
	h.profileID = pid
	if a.isCoderConversation(pid, conversationID) {
		resp, err := a.chatSup.startHostTurn(h, message, attachments)
		if err != nil {
			a.chatSup.release(conversationID, turnID)
			return a.chatBindingError(err)
		}
		return resp
	}
	resp, err := a.chatSup.startTurn(h, message, tools, allowRuns, attachments)
	if err != nil {
		a.chatSup.release(conversationID, turnID)
		return a.chatBindingError(err)
	}
	return resp
}

// StopChatTurn requests cancellation of a turn. Idempotent: stopping an
// already-finished turn, or an unknown turn id, is a harmless no-op
// success. A turn that is ACTIVE but owned by a different live instance is
// neither: nothing here would stop it, so that case reports an explicit
// failure instead. See isForeignActiveTurn.
func (a *App) StopChatTurn(conversationID, turnID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return a.chatBindingError(fmt.Errorf("chat supervisor not initialized"))
	}
	if h := a.chatSup.lookup(conversationID, turnID); h != nil {
		if h.host {
			return a.stopHostTurn(conversationID)
		}
		h.requestStop()
		return `{"ok":true}`
	}
	var rec ai.TurnRecord
	if err := a.chatCLI(&rec, "chat", "history", "turn", conversationID, turnID); err == nil {
		if rec.Status == "active" && isHostOwner(rec.OwnerInstanceID) {
			return a.stopHostTurn(conversationID)
		}
		if isForeignActiveTurn(rec.Turn(), a.chatSup.instanceID) {
			return `{"ok":false,"error":"this turn is running in another window and can only be stopped there"}`
		}
	}
	return `{"ok":true}`
}

// StopChatAgent stops one worker of a running dynamic-org turn (#255),
// leaving the lead and the other workers running, through `monoagentcli
// chat turn stop --agent`. That reaches the turn's process whichever
// window runs it. It returns the CLI's result: the worker's status after
// the stop (cancelled, or what it had already finished with) and whether a
// stop was sent. The stage updates from the journal's agent events. The ids
// go after "--" (and the agent as --agent=), so none can read as a flag.
func (a *App) StopChatAgent(conversationID, turnID, agentID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return a.chatBindingError(fmt.Errorf("chat supervisor not initialized"))
	}
	var res map[string]any
	if err := a.chatCLI(&res, "chat", "turn", "stop", "--agent="+agentID, "--wait", "20s", "--", conversationID, turnID); err != nil {
		return a.chatBindingError(err)
	}
	res["ok"] = true
	b, _ := json.Marshal(res)
	return string(b)
}

type chatConversationPage struct {
	Items      []ai.ConversationRecord `json:"items"`
	NextCursor string                  `json:"next_cursor"`
}

// ListChatConversations returns this profile's conversations, most recent
// first.
func (a *App) ListChatConversations(cursor string, limit int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return `{"items":[]}`
	}
	args := []string{"chat", "history", "list", "--limit", strconv.Itoa(clampChatLimit(limit))}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	var page chatConversationPage
	if err := a.chatCLI(&page, args...); err != nil {
		return a.chatBindingError(err)
	}
	items := make([]ai.Conversation, 0, len(page.Items))
	for _, r := range page.Items {
		items = append(items, r.Conversation())
	}
	b, _ := json.Marshal(map[string]any{"items": items, "nextCursor": page.NextCursor})
	return string(b)
}

type chatTurnPage struct {
	Items      []ai.TurnRecord `json:"items"`
	NextCursor string          `json:"next_cursor"`
}

// GetChatTurns returns one conversation's turns, most recent first. Each
// item carries ownedByThisInstance: true for every turn except one that is
// active AND owned by a different live instance — see isForeignActiveTurn.
func (a *App) GetChatTurns(conversationID, cursor string, limit int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return `{"items":[]}`
	}
	args := []string{"chat", "history", "turns", conversationID, "--limit", strconv.Itoa(clampChatLimit(limit))}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	var page chatTurnPage
	if err := a.chatCLI(&page, args...); err != nil {
		// Unknown (or deleted) conversation: no turns, as before, plus
		// notFound so a caller holding on to it (a restored coder bubble)
		// can let it go.
		if chatCLIExitCode(err) == 2 {
			return `{"items":[],"nextCursor":"","notFound":true}`
		}
		return a.chatBindingError(err)
	}
	hostPID, _ := a.profileID()
	out := make([]chatTurnListItem, 0, len(page.Items))
	for _, r := range page.Items {
		t := r.Turn()
		if t.Status == "active" && isHostOwner(t.OwnerInstanceID) {
			a.chatSup.followHostTurn(hostPID, conversationID, t.ID, t.LastCommittedSeq)
		}
		out = append(out, chatTurnListItem{Turn: t, OwnedByThisInstance: !isForeignActiveTurn(t, a.chatSup.instanceID)})
	}
	b, _ := json.Marshal(map[string]any{"items": out, "nextCursor": page.NextCursor})
	return string(b)
}

type chatEventPage struct {
	Items            []chatevents.Record `json:"items"`
	LastCommittedSeq int64               `json:"last_committed_seq"`
	HasMore          bool                `json:"has_more"`
}

// GetChatEvents returns one turn's events after afterSeq, ascending.
func (a *App) GetChatEvents(conversationID, turnID string, afterSeq int64, limit int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return `{"items":[]}`
	}
	var page chatEventPage
	err := a.chatCLI(&page, "chat", "history", "events", conversationID, turnID,
		"--after-seq", strconv.FormatInt(afterSeq, 10), "--limit", strconv.Itoa(clampChatLimit(limit)))
	if err != nil {
		if chatCLIExitCode(err) == 2 { // unknown turn: no events, as before
			return `{"items":[],"lastCommittedSeq":0,"hasMore":false}`
		}
		return a.chatBindingError(err)
	}
	events := make([]chatevents.Event, 0, len(page.Items))
	for _, r := range page.Items {
		events = append(events, r.Event())
	}
	b, _ := json.Marshal(map[string]any{
		"items":            events,
		"lastCommittedSeq": page.LastCommittedSeq,
		"hasMore":          page.HasMore,
	})
	return string(b)
}

// DeleteChatConversation removes a conversation and its turns/events —
// refused while a turn is active.
func (a *App) DeleteChatConversation(conversationID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if a.chatSup == nil {
		return a.chatBindingError(fmt.Errorf("chat supervisor not initialized"))
	}
	if err := a.chatCLI(nil, "chat", "history", "delete", conversationID); err != nil {
		return a.chatBindingError(err)
	}
	return `{"ok":true}`
}

func clampChatLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

// wailsChatEmitter is the real emitter used outside tests: emits the
// committed event as-is on the "chat:event" channel the frontend
// subscribes to.
func wailsChatEmitter(ctx context.Context) chatEventEmitter {
	return func(ev chatevents.Event) {
		runtime.EventsEmit(ctx, "chat:event", ev)
	}
}

// initChatSupervisor wires a.chatSup (called from startup()) and runs the
// orphaned-turn sweep in the background, so a slow CLI never delays
// startup. The supervisor reaches the history only through the CLI.
func (a *App) initChatSupervisor() {
	a.chatSup = newChatSupervisor(defaultChatProcessLauncher, wailsChatEmitter(a.ctx), findMonoAgentCLI)
	sup := a.chatSup
	go func() {
		for _, e := range sup.reconcileOrphanedTurns() {
			a.emitLog("SYSTEM", "WARN", fmt.Sprintf("chat turn reconciliation: %v", e))
		}
	}()
}
