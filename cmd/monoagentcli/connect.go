package main

import (
	"github.com/spf13/cobra"

	connectcli "github.com/monoes/mono-agent/internal/cli/connect"
	"github.com/monoes/mono-agent/internal/resources"
	"github.com/monoes/mono-agent/internal/storage"
)

// `connect` lives in internal/cli/connect; this is its adapter: the CLI's globals and helpers
// handed over as connectcli.Deps.

// newResourcesClient builds the provider client `connect resources` uses;
// tests swap it for one pointed at local servers.
var newResourcesClient = resources.New

func newConnectCmd(cfg *globalConfig) *cobra.Command {
	return connectcli.NewCommand(connectcli.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		ProfileID:         func() string { return cfg.ProfileID },
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		NotFound:          errNotFound,
		InvalidInput:      errInvalidInput,
		AuthFailed:        errAuthConnection,
		WriteJSON:         writeJSONTo,
		ReadSecretValue:   readSecretValue,
		ScrubSecrets:      scrubSecrets,
		ConnectionSecrets: connectionSecrets,
		ResourcesClient:   func() *resources.Client { return newResourcesClient() },
	})
}
