package main

import (
	"context"
	"database/sql"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/accountcli"
	"github.com/monoes/mono-agent/internal/library"
)

// `account` (login, logout, status), the sign-in it shares with `library login`, the
// first-run adoption of an older library login and the serving commands' refresher live in
// internal/cli/accountcli; this is their adapter. The guard's installation and the gate
// stay in main.go, account_run.go and account_gate.go.

func newAccountCmd(cfg *globalConfig) *cobra.Command {
	return accountcli.NewCommand(accountcli.Deps{
		JSONOutput:     func() bool { return cfg.JSONOutput },
		WithJSONErrors: func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) },
		AddSignIn: func(cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
			return addSignIn(cfg, cmd, name)
		},
	})
}

// addSignIn adds the sign-in flags to cmd and returns the sign-in they ask for (`account
// login` and `library login`).
func addSignIn(cfg *globalConfig, cmd *cobra.Command, name string) func(*cobra.Command) (account.Status, bool, error) {
	return accountcli.SignIn{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		OpenLoginURL: openLoginURLGated,
	}.Add(cmd, name)
}

// adoptionSetting is the settings row that says a database has had its one try (spec D23).
const adoptionSetting = accountcli.AdoptionSetting

// adoptOlderLogin is library.AdoptIntoAccount, a variable so that a test can see whether the wiring
// calls it without a monoes.me to call.
var adoptOlderLogin = library.AdoptIntoAccount

// adoptFirstRun is accountcli.AdoptFirstRun on the command's --db-path.
func adoptFirstRun(cmd *cobra.Command, cfg *globalConfig) {
	accountcli.AdoptFirstRun(cmd, cfg.DBPath, func(ctx context.Context, db *sql.DB, g *account.Guard, report func(string, library.Outcome)) (bool, error) {
		return adoptOlderLogin(ctx, db, g, report)
	})
}

// adoptBeforeGate runs the adoption ahead of the gate (accountcli.AdoptBeforeGate), on a
// throwaway tree of this CLI, with its lines on stderr.
func adoptBeforeGate(ctx context.Context, args []string, stderr io.Writer) {
	accountcli.AdoptBeforeGate(ctx, args, stderr, func() *cobra.Command {
		probe := newProbeRootCmd()
		applyClassification(probe)
		return probe
	}, func(cmd *cobra.Command, dbPath string) { adoptFirstRun(cmd, &globalConfig{DBPath: dbPath}) })
}

// startServingGuard is accountcli.StartServingGuard; a test replaces it.
var startServingGuard = accountcli.StartServingGuard

// servingCommand marks c as a command that serves for as long as it runs
// (accountcli.ServingCommand): its pre-run starts the account guard's refresher.
func servingCommand(c *cobra.Command, when ...func(*cobra.Command) bool) *cobra.Command {
	return accountcli.ServingCommand(c, func(ctx context.Context) { startServingGuard(ctx) }, when...)
}
