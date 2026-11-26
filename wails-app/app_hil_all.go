package main

// HILPendingAll is the Human in Loop page's All profiles view: `hil pending
// --all` handed back untouched, every profile's pending items, each with its
// profile_id and profile_name. It only reads: an item is decided through the
// profile-scoped bindings. It returns the CLI's JSON or {"error": "..."}.
func (a *App) HILPendingAll() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runRawCLI(summaryCLITimeout, unscopedCLIArgs("hil", "pending", "--all"))
}

// HILOrgPendingAll is the same view's organisation items: `org pending --all`
// handed back untouched, every question, approval and gate the orgs of every
// profile wait on, each item and error with its profile. It only reads. It
// starts monomind for each org, so it gets the org timeout, which is longer
// than the CLI's own limit for the listing.
func (a *App) HILOrgPendingAll() string {
	if err := a.signedOut(); err != nil {
		return loginRequiredJSON(err)
	}
	return a.runRawCLI(orgCLITimeout, unscopedCLIArgs("org", "pending", "--all"))
}
