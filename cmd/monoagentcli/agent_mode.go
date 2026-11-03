package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/agentmode"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/httpapi"
	"github.com/monoes/mono-agent/internal/storage"
)

// newAgentModeCmd is `agent-mode`; it lives in internal/cli/agentmode, this hands it the CLI's globals.
func newAgentModeCmd(cfg *globalConfig) *cobra.Command {
	return agentmode.NewCommand(agentmode.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:     errNotFound,
		InvalidInput: errInvalidInput,
		WriteJSON:    writeJSONTo,
		Refusal:      opgate.Refusal,
		APIAddr:      func() string { return httpapi.ResolveAddr("") },
	})
}
