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
