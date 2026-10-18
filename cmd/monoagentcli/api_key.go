package main

import (
	"github.com/spf13/cobra"

	apikeycli "github.com/monoes/mono-agent/internal/cli/apikey"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
)

// `api key create|list|show|update|revoke` lives in internal/cli/apikey; this is its adapter:
// the CLI's globals and helpers handed over as apikeycli.Deps.

// apiStdinIsTerminal decides whether `api key revoke` may ask instead of
// requiring --yes. Tests replace it.
var apiStdinIsTerminal = stdinIsTerminal

// newAPIKeyCmd groups the lifecycle of the active profile's API keys. A key
// belongs to one profile and authenticates /v1 requests as that profile only.
func newAPIKeyCmd(cfg *globalConfig) *cobra.Command {
	return apikeycli.NewCommand(apikeycli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		ProfileID:       func() string { return cfg.ProfileID },
		OpenDB:          func() (*storage.Database, error) { return initDB(cfg) },
		StdinIsTerminal: func() bool { return apiStdinIsTerminal() },
		NotFound:        errNotFound,
		InvalidInput:    errInvalidInput,
		WriteJSON:       writeJSONTo,
		Refusal:         operator.Refusal,
	})
}
