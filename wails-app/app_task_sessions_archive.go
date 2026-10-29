package main

// TaskSessionArchiveCascade is `task session archive ID --cascade`: the operator, having
// confirmed in the page, archives a session with every session below it. The CLI decides
// everything (running ones are stopped first, an agent can never do this); the page only
// renders the archived ids or the refusal.
func (a *App) TaskSessionArchiveCascade(id int64) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	sid, bad := sessionIDArg(id)
	if bad != "" {
		return bad
	}
	return a.taskCLI("", "session", "archive", sid, "--cascade")
}
