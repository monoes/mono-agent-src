package main

import (
	"github.com/spf13/cobra"

	doctorcli "github.com/monoes/mono-agent/internal/cli/doctor"
	"github.com/monoes/mono-agent/internal/health"
)

// doctorWithoutAMachine is `doctor` over an empty environment: a fix that reaches the machine (registering the
// MCP server, the daemon's service) answers "not available here" instead of acting, so the operator's pass
// changes nothing and starts nothing.
func doctorWithoutAMachine(cfg *globalConfig) *cobra.Command {
	d := doctorDeps(cfg)
	d.NewEnv = func([]string) (*health.Env, func()) { return &health.Env{}, func() {} }
	return doctorcli.NewDoctorCommand(d)
}

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
		{path: "daemon install", build: newDaemonCmd, args: []string{"install"}, recovery: true},
		{path: "daemon uninstall", build: newDaemonCmd, args: []string{"uninstall"}, recovery: true},
		{path: "daemon restart", build: newDaemonCmd, args: []string{"restart"}, recovery: true},
		{path: "update", build: newUpdateCmd, args: []string{"--current", "v0.0.1"}, recovery: true},
		{path: "update", build: newUpdateCmd, args: []string{"--app", "/nonexistent/MonoAgent"}, recovery: true},
		{path: "update", build: newUpdateCmd, args: []string{"--force", "--current", "v0.0.1"}, recovery: true},
		{path: "doctor fix", build: doctorWithoutAMachine, args: []string{"fix", health.FixMCPRegister}, recovery: true},
		{path: "doctor fix", build: doctorWithoutAMachine, args: []string{"fix", health.FixAutostart}, recovery: true},
		{path: "doctor", build: doctorWithoutAMachine, args: []string{"--fix", "--yes", "--group", "no-such-group"}, recovery: true},
		{path: "setup", build: newSetupCmd, args: []string{"--group", "no-such-group"}, recovery: true},
		{path: "library publish", build: newLibraryCmd, args: []string{"publish", "workflow", "no-such-workflow"}},
	}
}
