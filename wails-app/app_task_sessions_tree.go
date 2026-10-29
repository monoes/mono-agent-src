// wails-app/app_task_sessions_tree.go
//
// Making and renaming a session from the Tasks tab. Like the other session
// bindings (app_task_sessions.go) each runs `monoagentcli --profile <active>
// --json task session …` and returns the CLI's session document verbatim, or
// its {"error","code"}; the title length, the parent's depth and the kind are
// the CLI's rules and its refusal is what the page shows. User text goes in the
// --flag=value form or after "--", so text that starts with a dash is never a flag.
package main

import "strings"

// TaskSessionCreate is `task session new --title=T [--goal=G] [--parent=ID] [--kind=K]`.
// parentID 0 and an empty kind leave the CLI's defaults (a root session, kind task).
func (a *App) TaskSessionCreate(title, goal string, parentID int64, kind string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	if strings.TrimSpace(title) == "" {
		return taskRefusal("a session needs a title")
	}
	args := []string{"session", "new", "--title=" + title}
	if strings.TrimSpace(goal) != "" {
		args = append(args, "--goal="+goal)
	}
	if parentID < 0 {
		return taskRefusal("a parent session id is a positive number")
	}
	if parentID > 0 {
		sid, _ := sessionIDArg(parentID)
		args = append(args, "--parent="+sid)
	}
	if k := strings.TrimSpace(kind); k != "" {
		args = append(args, "--kind="+k)
	}
	return a.taskCLI("", args...)
}

// TaskSessionRename is `task session rename ID -- TITLE`.
func (a *App) TaskSessionRename(id int64, title string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	if strings.TrimSpace(title) == "" {
		return taskRefusal("a session needs a title")
	}
	return a.taskCLI("", "session", "rename", sid, "--", title)
}
