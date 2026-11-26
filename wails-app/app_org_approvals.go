package main

import "errors"

// ListOrgPending is every question, approval and gate the profile's orgs wait
// on a person for (`org pending`).
func (a *App) ListOrgPending() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runOrgCLI("pending")
}

// ApproveOrgAction approves one approval request, the one the person saw,
// by its id.
func (a *App) ApproveOrgAction(name, role, action, requestID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.resolveOrgRequest("approve", name, role, action, requestID)
}

// DenyOrgAction denies one approval request by its id.
func (a *App) DenyOrgAction(name, role, action, requestID string) string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.resolveOrgRequest("deny", name, role, action, requestID)
}

// resolveOrgRequest never uses the role/action form of `org approve|deny`,
// which also resolves calls queued after the person looked. The values go
// after "--", so none of them is read as a flag.
func (a *App) resolveOrgRequest(verb, name, role, action, requestID string) string {
	if requestID == "" {
		return aiError(errors.New("this approval request has no id, so the app cannot resolve it on its own; update monomind"))
	}
	return a.runOrgCLI(verb, "--request="+requestID, "--", name, role, action)
}
