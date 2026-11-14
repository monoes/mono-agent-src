package main

import "github.com/spf13/cobra"

// operatorOnlyRowsPR6 are the verbs of class C of issue 1475 that the owner decided are operator-only:
// the two monoes.me logouts, the top-level `logout` (saved platform sessions), `release keygen`, the daemon's
// install, uninstall and restart, `update` (everything but --check), `setup` and `library publish`. Each
// args value reaches its gate before anything else and is harmless for the operator's pass: a flag mix the
// verb refuses for everyone after the gate (`release keygen --force --stdout-private`, `update --current`
// without --check or --app), a nothing-to-find argument (`logout`, `library publish`), or a group that
// selects no check (`setup`). The daemon verbs go through the test installer (api_config_test.go).
func operatorOnlyRowsPR6() []opRow {
	return []opRow{
		{path: "account logout", build: newAccountCmd, args: []string{"logout"}},
		{path: "library logout", build: newLibraryCmd, args: []string{"logout"}},
		{path: "logout", build: newLogoutCmd, args: []string{"no-such-platform"}},
		{path: "release keygen", build: func(*globalConfig) *cobra.Command { return newReleaseCmd() }, args: []string{"keygen", "--force", "--stdout-private"}},
		{path: "daemon install", build: newDaemonCmd, args: []string{"install"}},
		{path: "daemon uninstall", build: newDaemonCmd, args: []string{"uninstall"}},
		{path: "daemon restart", build: newDaemonCmd, args: []string{"restart"}},
		{path: "update", build: newUpdateCmd, args: []string{"--current", "v0.0.1"}},
		{path: "setup", build: newSetupCmd, args: []string{"--group", "no-such-group"}},
		{path: "library publish", build: newLibraryCmd, args: []string{"publish", "workflow", "no-such-workflow"}},
	}
}
