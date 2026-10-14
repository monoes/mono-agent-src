package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	summarycli "github.com/monoes/mono-agent/internal/cli/summary"
	"github.com/monoes/mono-agent/internal/storage"
)

// `summary` lives in internal/cli/summary; this is its adapter: the CLI's globals and helpers
// handed over as summarycli.Deps.

func newSummaryCmd(cfg *globalConfig) *cobra.Command {
	return summarycli.NewCommand(summarycli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		InvalidInput: errInvalidInput,
		WriteJSON:    writeJSONTo,
		ProbeAddrs:   extensionProbeAddrs,
		OpenAutomationRegistry: func() (*automation.Registry, error) {
			return openAutomationRegistry()
		},
	})
}
