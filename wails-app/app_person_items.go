// wails-app/app_person_items.go
//
// The person items a coder session raises for the person (a giant session's plan waits with no
// timeout): the list, and the three ways to answer. The GUI only renders; each binding runs
// `monoagentcli --profile <active> --json task session person …` and returns the CLI's document
// verbatim, or its {"error","code"}. Who may answer, which session has an item and what each
// answer does are the CLI's rules. User text goes in the --flag=value form, so text that starts
// with a dash is never a flag.
package main

import "strings"

// PersonItemsList is `task session person inbox`: {"items":[…]} with each item's kind, session,
// agent chat, why it is giant work and its numbers.
func (a *App) PersonItemsList() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.taskCLI("", "session", "person", "inbox")
}

// PersonItemApprove is `task session person approve ID`.
func (a *App) PersonItemApprove(sessionID int64) string {
	return a.personAnswer(sessionID, "", "approve")
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
