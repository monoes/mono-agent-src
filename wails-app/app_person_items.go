// wails-app/app_person_items.go
//
// The person items a coder session raises for the person (a giant session's plan waits with no
// timeout): the list, and the three ways to answer. The GUI only renders; each binding runs
// `monoagentcli --profile <active> --json task session person …` and returns the CLI's document
// verbatim, or its {"error","code"}. Who may answer, which session has an item and what each
// answer does are the CLI's rules. User text goes in the --flag=value form, so text that starts
// with a dash is never a flag.
package main

import (
	"strconv"
	"strings"
)

// PersonItemsList is `task session person inbox`: {"items":[…]} with each item's kind, session,
// agent chat, why it is giant work and its numbers.
func (a *App) PersonItemsList() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.taskCLI("", "session", "person", "inbox")
}

// PersonItemApprove is `task session person approve ID --rev=N [--digest=HASH]`: the revision and
// digest are the ones the inbox showed, so a plan that changed since is refused (code plan_changed).
func (a *App) PersonItemApprove(sessionID, rev int64, digest string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(sessionID)
	if bad != "" {
		return bad
	}
	if rev <= 0 {
		return taskRefusal("the plan revision you read is missing: reload and read the plan again")
	}
	args := []string{"session", "person", "approve", sid, "--rev=" + strconv.FormatInt(rev, 10)}
	if d := strings.TrimSpace(digest); d != "" {
		args = append(args, "--digest="+d)
	}
	return a.taskCLI("", args...)
}

// PersonItemDeny is `task session person deny ID --note=TEXT`.
func (a *App) PersonItemDeny(sessionID int64, note string) string {
	return a.personAnswer(sessionID, note, "deny", "--note=")
}

// PersonItemAsk is `task session person ask ID --text=TEXT`.
func (a *App) PersonItemAsk(sessionID int64, text string) string {
	return a.personAnswer(sessionID, text, "ask", "--text=")
}

func (a *App) personAnswer(sessionID int64, words, verb string, flag ...string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(sessionID)
	if bad != "" {
		return bad
	}
	args := []string{"session", "person", verb, sid}
	if len(flag) > 0 {
		if strings.TrimSpace(words) == "" {
			return taskRefusal("write your words first")
		}
		args = append(args, flag[0]+words)
	}
	return a.taskCLI("", args...)
}

// PersonItemShow is `task session person full ID`: the whole plan of one waiting item, no field
// cut, with the rev and digest the inbox carries.
func (a *App) PersonItemShow(sessionID int64) string {
	return a.personAnswer(sessionID, "", "full")
}
