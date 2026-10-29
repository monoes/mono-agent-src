// wails-app/app_history.go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/monoes/mono-agent/internal/ai"
)

// The unified history and the access-aware chat bindings. Each shells out to
// `monoagentcli chat history …`; the CLI's JSON is the contract.

// HistoryFilter narrows ListHistory; every field is optional.
type HistoryFilter struct {
	Kind        string `json:"kind"` // chat or org
	Status      string `json:"status"`
	Application string `json:"application"`
	Query       string `json:"query"`
}

// HistoryRow is one conversation or one org run. The GUI computes unread.
type HistoryRow struct {
	ID              string   `json:"id"`
	Kind            string   `json:"kind"`
	Title           string   `json:"title"`
	Status          string   `json:"status"` // running, done, error, needs or idle
	UpdatedAt       string   `json:"updatedAt"`
	Runtime         string   `json:"runtime"`
	Model           string   `json:"model"`
	Effort          string   `json:"effort"`
	Access          string   `json:"access"`
	Mode            string   `json:"mode"`
	OrgMode         string   `json:"orgMode"`
	Cwd             string   `json:"cwd"`
	WorkflowContext string   `json:"workflowContext"`
	TaskID          string   `json:"taskId"`
	TaskStatus      string   `json:"taskStatus"`
	TaskGoal        string   `json:"taskGoal"`
	ApplicationID   string   `json:"applicationId"`
	OrgName         string   `json:"orgName"`
	RunID           string   `json:"runId"`
	CostUSD         *float64 `json:"costUsd"`
}

// HistoryPage is a page of rows; NextCursor is "" at the end. Warnings name
// the orgs whose runs could not be read.
type HistoryPage struct {
	Items      []HistoryRow `json:"items"`
	NextCursor string       `json:"next_cursor"`
	Warnings   []string     `json:"warnings"`
}

// ListHistory is `chat history list --all`: chats and org runs, newest first.
func (a *App) ListHistory(cursor string, limit int, filter HistoryFilter) (HistoryPage, error) {
	if err := a.signedOut(); err != nil {
		return HistoryPage{}, err
	}
	args := []string{"list", "--all", "--limit", fmt.Sprint(clampChatLimit(limit))}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	for _, f := range [][2]string{{"--kind", filter.Kind}, {"--status", filter.Status}, {"--application", filter.Application}, {"--query", filter.Query}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	var page HistoryPage
	if err := a.historyCLI(&page, args...); err != nil {
		return HistoryPage{}, err
	}
	if page.Items == nil {
		page.Items = []HistoryRow{}
	}
	if page.Warnings == nil {
		page.Warnings = []string{}
	}
	return page, nil
}

// Conversation is a chat conversation as the GUI keeps it.
type Conversation struct {
	ID              string `json:"id"`
	ProfileID       string `json:"profileId"`
	Backend         string `json:"backend"`
	WorkflowContext string `json:"workflowContext"`
	RuntimeID       string `json:"runtimeId"`
	Model           string `json:"model"`
	Effort          string `json:"effort"`
	Mode            string `json:"mode"`
	Access          string `json:"access"`
	Cwd             string `json:"cwd"`
	OrgMode         string `json:"orgMode"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

func conversationOf(r ai.ConversationRecord) Conversation {
	return Conversation{ID: r.ID, ProfileID: r.ProfileID, Backend: r.Backend, WorkflowContext: r.WorkflowContext,
		RuntimeID: r.RuntimeID, Model: r.Model, Effort: r.Effort, Mode: r.Mode, Access: r.Access, Cwd: r.Cwd,
		OrgMode: r.OrgMode, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// CreateChatConversationAccess creates a conversation at an access level
// (scoped, coding, workspace or full; "" takes the coder default: the one the
// operator set, else full, when coder mode is ready).
// Above scoped it works in cwd, or in the profile's folder when cwd is empty. The GUI window runs as the operator, so any level is allowed.
func (a *App) CreateChatConversationAccess(workflowID, runtimeID, model, effort, access, cwd string) (Conversation, error) {
	if err := a.signedOut(); err != nil {
		return Conversation{}, err
	}
	if workflowID == "" {
		workflowID = "general"
	}
	args := []string{"create", "--workflow", workflowID}
	if runtimeID != "" {
		args = append(args, "--runtime", runtimeID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	if access != "" {
		args = append(args, "--access", access)
		if access != ai.AccessScoped {
			if cwd != "" {
				args = append(args, "--cwd", cwd)
			} else {
				args = append(args, "--profile-folder")
			}
		}
	}
	var rec ai.ConversationRecord
	if err := a.historyCLI(&rec, args...); err != nil {
		return Conversation{}, err
	}
	return conversationOf(rec), nil
}

// Survivor is a process a lowering could not stop.
type Survivor struct {
	Pid     int    `json:"pid"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
}

// LoweredEffects is what lowering a conversation's access did.
type LoweredEffects struct {
	Stopped        []int      `json:"stopped"`
	Survivors      []Survivor `json:"survivors"`
	WorkersDropped bool       `json:"workersDropped"`
	TaskRevoked    bool       `json:"taskRevoked"`
}

// AccessChange is the result of SetChatAccess. OK is false, with Code and
// Current, when the change was refused before the CLI was asked: access_changed
// (the real level is not the one the caller saw), expected_required,
// confirm_required (a raise without ConfirmedRaise). Lowered is set for a
// lowering; its Survivors are processes that could not be stopped, which is a
// lowering that happened, not a failure.
type AccessChange struct {
	OK             bool            `json:"ok"`
	Code           string          `json:"code"`
	Current        string          `json:"current"`
	Conversation   Conversation    `json:"conversation"`
	PreviousAccess string          `json:"previousAccess"`
	Lowered        *LoweredEffects `json:"lowered"`
}

// recordAccess is a conversation row's level, from its mode for an old row.
func recordAccess(r ai.ConversationRecord) string {
	if ai.ValidAccess(r.Access) {
		return r.Access
	}
	return ai.AccessForMode(r.Mode)
}

// SetChatAccess is `chat history set-access`, guarded at this boundary: it
// reads the conversation's real level from the CLI first and refuses when that
// is not expectedCurrent (the level the UI believes; always required, so a
// stale cached level can't change anything), and refuses a raise unless
// confirmedRaise. The GUI window is the operator, so the CLI would let it
// raise; the confirm is the user's click on the raise prompt. A lowering needs
// no confirm. The change itself goes with --expect, so a level that changes
// between this read and the CLI's update is refused by the CLI
// (access_changed) and nothing is done. A scoped conversation raised
// gets the profile's folder.
func (a *App) SetChatAccess(id, level, expectedCurrent string, confirmedRaise bool) (AccessChange, error) {
	if err := a.signedOut(); err != nil {
		return AccessChange{}, err
	}
	if !ai.ValidAccess(level) {
		return AccessChange{}, fmt.Errorf("unknown access %q (scoped, coding, workspace or full)", level)
	}
	if expectedCurrent == "" {
		return AccessChange{Code: "expected_required"}, nil
	}
	var cur ai.ConversationRecord
	if err := a.historyCLI(&cur, "show", id); err != nil {
		return AccessChange{}, err
	}
	real := recordAccess(cur)
	if real != expectedCurrent {
		return AccessChange{Code: "access_changed", Current: real}, nil
	}
	if ai.AccessRank(level) > ai.AccessRank(real) && !confirmedRaise {
		return AccessChange{Code: "confirm_required", Current: real}, nil
	}
	args := []string{"set-access", id, level, "--expect", expectedCurrent}
	if cur.Cwd == "" && level != ai.AccessScoped {
		args = append(args, "--profile-folder")
	}
	var raw struct {
		ai.ConversationRecord
		Previous string `json:"previous_access"`
		Lowered  *struct {
			Stopped        []int      `json:"stopped"`
			Survivors      []Survivor `json:"survivors"`
			WorkersDropped bool       `json:"workers_dropped"`
			TaskRevoked    bool       `json:"task_revoked"`
		} `json:"lowered"`
	}
	if err := a.historyCLIKeepingOutput(&raw, args...); err != nil {
		// The CLI is the authority: it applies the change only at the level
		// the caller expected, and says what the level is when it is not.
		var ae *aitaskError
		if errors.As(err, &ae) && ae.code == "access_changed" {
			return AccessChange{Code: "access_changed", Current: ae.current}, nil
		}
		return AccessChange{}, err
	}
	out := AccessChange{OK: true, Current: recordAccess(raw.ConversationRecord), Conversation: conversationOf(raw.ConversationRecord), PreviousAccess: raw.Previous}
	if l := raw.Lowered; l != nil {
		out.Lowered = &LoweredEffects{Stopped: l.Stopped, Survivors: l.Survivors, WorkersDropped: l.WorkersDropped, TaskRevoked: l.TaskRevoked}
		if out.Lowered.Stopped == nil {
			out.Lowered.Stopped = []int{}
		}
		if out.Lowered.Survivors == nil {
			out.Lowered.Survivors = []Survivor{}
		}
	}
	return out, nil
}

// ChatConversationRecord is what the chat pane needs of one conversation. OK
// is false with Code "not_found" for an unknown id.
type ChatConversationRecord struct {
	OK              bool   `json:"ok"`
	Code            string `json:"code"`
	ID              string `json:"id"`
	Title           string `json:"title"`
	Access          string `json:"access"`
	Mode            string `json:"mode"`
	Runtime         string `json:"runtime"`
	Model           string `json:"model"`
	Effort          string `json:"effort"`
	Cwd             string `json:"cwd"`
	OrgMode         string `json:"orgMode"`
	WorkflowContext string `json:"workflowContext"`
}

// GetChatConversationRecord reads one conversation from the CLI
// (`chat history list --all --id`), with the same redacted title the history
// list shows.
func (a *App) GetChatConversationRecord(id string) (ChatConversationRecord, error) {
	if err := a.signedOut(); err != nil {
		return ChatConversationRecord{}, err
	}
	if id == "" {
		return ChatConversationRecord{Code: "not_found"}, nil
	}
	var page HistoryPage
	if err := a.historyCLI(&page, "list", "--all", "--id", id, "--limit", "1"); err != nil {
		return ChatConversationRecord{}, err
	}
	for _, r := range page.Items {
		if r.ID == id {
			return ChatConversationRecord{OK: true, ID: r.ID, Title: r.Title, Access: r.Access, Mode: r.Mode, Runtime: r.Runtime,
				Model: r.Model, Effort: r.Effort, Cwd: r.Cwd, OrgMode: r.OrgMode, WorkflowContext: r.WorkflowContext}, nil
		}
	}
	return ChatConversationRecord{Code: "not_found"}, nil
}

// DeleteChatResult says what DeleteChatConversations did, per id: Active ids
// have a turn running (stop it first), Failed ids carry the CLI's message.
type DeleteChatResult struct {
	Deleted []string          `json:"deleted"`
	Active  []string          `json:"active"`
	Failed  map[string]string `json:"failed"`
	// Kept ids were not deleted because the CLI declined (see DeleteKept): the survivors to name, or the
	// operator it asks for.
	Kept map[string]DeleteKept `json:"kept"`
}

// DeleteChatConversations deletes each conversation with the single delete,
// so a turn that is running refuses exactly as DeleteChatConversation does.
func (a *App) DeleteChatConversations(ids []string) (DeleteChatResult, error) {
	res := DeleteChatResult{Deleted: []string{}, Active: []string{}, Failed: map[string]string{}, Kept: map[string]DeleteKept{}}
	if err := a.signedOut(); err != nil {
		return res, err
	}
	for _, id := range ids {
		ans := a.historyDelete(id)
		var ae *aitaskError
		switch {
		case ans.Deleted:
			res.Deleted = append(res.Deleted, id)
		case ans.Kept != nil:
			res.Kept[id] = *ans.Kept
		case errors.As(ans.Err, &ae) && strings.Contains(ae.msg, "active turn"):
			res.Active = append(res.Active, id)
		default:
			res.Failed[id] = ans.Err.Error()
		}
	}
	return res, nil
}

// historyCLI runs `monoagentcli --profile P --json chat history <args>`.
func (a *App) historyCLI(result any, args ...string) error {
	full, err := a.scopedArgs(append([]string{"chat", "history"}, args...)...)
	if err != nil {
		return err
	}
	return a.jsonCLIInto(result, full)
}

// exitSurvivorsRemain is the CLI's exit for a lowering that took effect but
// left processes it could not stop; the result is still on stdout.
const exitSurvivorsRemain = 5

// historyCLIKeepingOutput is historyCLI for set-access: exit 5
// (survivors_remain) is not a failure to lower, so the result printed before
// it is decoded and returned, with the survivors in it.
func (a *App) historyCLIKeepingOutput(result any, args ...string) error {
	full, err := a.scopedArgs(append([]string{"chat", "history"}, args...)...)
	if err != nil {
		return err
	}
	bin, err := findMonoAgentCLI()
	if err != nil {
		return err
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, bin, full...)
	suppressConsole(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == exitSurvivorsRemain) {
		var ref aitaskRefusal
		_ = json.Unmarshal([]byte(lastLine(string(out))), &ref)
		msg := ref.Error
		if msg == "" {
			msg = cliStderr(stderr.Bytes())
		}
		if msg == "" {
			msg = err.Error()
		}
		return &aitaskError{msg: msg, code: ref.Code, fallback: ref.Fallback, current: ref.Current}
	}
	// The first JSON document is the result; a --json error document may follow.
	return json.NewDecoder(bytes.NewReader(out)).Decode(result)
}
