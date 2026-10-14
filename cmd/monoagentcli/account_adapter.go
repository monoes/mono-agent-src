package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/cli/accountcli"
)

// `account` (login, logout, status) lives in internal/cli/accountcli; this is its adapter.
// The sign-in it shares with `library login` stays in account_login.go.

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
	var f signInFlags
	f.add(cmd)
	return func(cmd *cobra.Command) (account.Status, bool, error) { return signIn(cmd, cfg, &f, name) }
}
