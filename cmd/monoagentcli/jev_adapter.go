package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/jevcli"
	"github.com/monoes/mono-agent/internal/storage"
)

// `jev` and the Jev retry triage (installed as workflow.RetryClassifier when the package
// loads) live in internal/cli/jevcli; this is the adapter that hands it the CLI's globals.

func newJevCmd(cfg *globalConfig) *cobra.Command {
	return jevcli.NewCommand(jevcli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		StdinIsTerminal: stdinIsTerminal,
	})
}
