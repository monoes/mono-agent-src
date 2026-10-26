package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentclient"
	aitaskcli "github.com/monoes/mono-agent/internal/cli/aitask"
	"github.com/monoes/mono-agent/internal/storage"
)

// `aitask` lives in internal/cli/aitask; this is its adapter: the CLI's globals and helpers
// handed over as aitaskcli.Deps.
func newAitaskCmd(cfg *globalConfig) *cobra.Command {
	wireAitaskKinds(cfg)
	return aitaskcli.New(aitaskcli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		DBPath:          func() string { return cfg.DBPath },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		RequireOperator: requireOperator,
		API:             agentclient.FromEnv,
	})
}
