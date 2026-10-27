// wails-app/app_task_sessions.go
//
// The Tasks tab's session bindings (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md
// §5, §6). Each runs `monoagentcli --profile <active> --json task …` through
// taskCLI and returns the CLI's document verbatim, or its {"error","code"}
// on a refusal, so the page can tell operator_only from not_found. The app
// is the operator's context: approve, resume, budget and archive are refused
// by the CLI itself when the app was started from an agent's shell. The one
// read made in process, TaskSessionPulse, is with the board watcher in
// app_tasks_watch.go.
package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// sessionIDArg renders a session id, or the refusal for one that is not positive.
func sessionIDArg(id int64) (string, string) {
	if id <= 0 {
		return "", taskRefusal("a session id is a positive number")
	}
	return strconv.FormatInt(id, 10), ""
}

// TaskSessions is `task session list`, archived ones too with all.
func (a *App) TaskSessions(all bool) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	args := []string{"session", "list"}
	if all {
		args = append(args, "--all")
	}
	return a.taskCLI("", args...)
}

// TaskSessionShow is `task session show ID --events N`: the session and its history.
func (a *App) TaskSessionShow(id int64, events int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	if events < 0 {
		events = 0
	}
	return a.taskCLI("", "session", "show", sid, "--events", strconv.Itoa(events))
}

// TaskSessionPlan is `task session plan ID`: the tasks with dependencies and acceptance.
func (a *App) TaskSessionPlan(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	return a.taskCLI("", "session", "plan", sid)
}

// TaskSessionBoard is `task board --session ID --done-limit N`: the session's five columns.
func (a *App) TaskSessionBoard(id int64, doneLimit int) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	return a.taskCLI("", "board", "--session", sid, "--done-limit", strconv.Itoa(boardLimit(doneLimit)))
}

// sessionVerb runs `task session VERB ID`.
func (a *App) sessionVerb(verb string, id int64) string {
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	return a.taskCLI("", "session", verb, sid)
}

// TaskSessionApprove approves the draft and starts its bound lead when it has
// a conversation. A manual session without a conversation keeps its CLI behavior.
func (a *App) TaskSessionApprove(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.continueTaskSession(a.sessionVerb("approve", id))
}

// TaskSessionPause is `task session pause ID`.
func (a *App) TaskSessionPause(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.sessionVerb("pause", id)
}

// TaskSessionResume resumes the session and its bound lead, if any.
func (a *App) TaskSessionResume(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.continueTaskSession(a.sessionVerb("resume", id))
}

// TaskSessionStop is `task session stop ID`: for good, every claim released.
func (a *App) TaskSessionStop(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.sessionVerb("stop", id)
}

// TaskSessionClose is `task session close ID [--note=TEXT]`: the operator closes
// a mission session its lead only proposed to close. The CLI decides if it may.
func (a *App) TaskSessionClose(id int64, note string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	args := []string{"session", "close", sid}
	if strings.TrimSpace(note) != "" {
		args = append(args, "--note="+note)
	}
	return a.taskCLI("", args...)
}

// TaskSessionArchive is `task session archive ID`.
func (a *App) TaskSessionArchive(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.sessionVerb("archive", id)
}

// maxSessionBudgetUSD bounds a budget the page may set.
const maxSessionBudgetUSD = 1e6

// sessionBudgetSpec is TaskSessionBudget's argument: the dollar budget (0 for
// none); a field left out stays as it is.
type sessionBudgetSpec struct {
	USD *float64 `json:"usd"`
}

// TaskSessionBudget is `task session budget ID --usd=N`, in the = form.
func (a *App) TaskSessionBudget(id int64, spec string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	var s sessionBudgetSpec
	if err := json.Unmarshal([]byte(spec), &s); err != nil {
		return taskRefusal("the budget is not valid JSON: " + err.Error())
	}
	if s.USD == nil {
		return taskRefusal("nothing to change: give usd")
	}
	if math.IsNaN(*s.USD) || math.IsInf(*s.USD, 0) || *s.USD < 0 || *s.USD > maxSessionBudgetUSD {
		return taskRefusal(fmt.Sprintf("a budget is a number from 0 (none) to %d dollars", int(maxSessionBudgetUSD)))
	}
	return a.taskCLI("", "session", "budget", sid, "--usd="+strconv.FormatFloat(*s.USD, 'f', -1, 64))
}
