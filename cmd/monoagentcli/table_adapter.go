package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/datatablecli"
	"github.com/monoes/mono-agent/internal/storage"
)

// `table` lives in internal/cli/datatablecli; this is its adapter.
func newTableCmd(cfg *globalConfig) *cobra.Command {
	return datatablecli.NewCommand(datatablecli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		RequireOperator: cfg.requireOperator,
	})
}
