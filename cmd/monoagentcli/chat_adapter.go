package main

import (
	"database/sql"

	"github.com/spf13/cobra"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/storage"
)

// `chat` and `coder` live in internal/cli/chat; this is their adapter: the CLI's globals and
// helpers handed over as chatcli.Deps, and the old names the rest of the CLI uses.

func chatDeps(cfg *globalConfig) chatcli.Deps {
	return chatcli.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		ProfileID:         func() string { return cfg.ProfileID },
		DBPath:            func() string { return cfg.DBPath },
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		ResolveProfileID:  resolveProfileID,
		RequireOperator:   requireOperator,
		PublicationPrompt: func() string { return publicationAgentPrompt(cfg) },
	}
}

func newChatCmd(cfg *globalConfig) *cobra.Command { return chatcli.NewChatCommand(chatDeps(cfg)) }

func newCoderCmd(cfg *globalConfig) *cobra.Command { return chatcli.NewCoderCommand(chatDeps(cfg)) }

func newCoderSetCmd(cfg *globalConfig) *cobra.Command {
	return chatcli.NewCoderSetCommand(chatDeps(cfg))
}

// capabilityProbe is monomind's capability handshake (coder's).
var capabilityProbe = chatcli.CapabilityProbe

// coderSettings are the coder-mode settings.
type coderSettings = chatcli.Settings

func loadCoderSettings(db *sql.DB) (coderSettings, error) { return chatcli.LoadSettings(db) }
