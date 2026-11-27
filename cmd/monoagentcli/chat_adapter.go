package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"

	"github.com/spf13/cobra"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/storage"
)

// `chat` and `coder` live in internal/cli/chat; this is their adapter: the CLI's globals and
// helpers handed over as chatcli.Deps, and the old names the rest of the CLI uses.

func chatDeps(cfg *globalConfig) chatcli.Deps {
	d := chatcli.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		ProfileID:         func() string { return cfg.ProfileID },
		DBPath:            func() string { return cfg.DBPath },
		OpenDB:            func() (*storage.Database, error) { return initDB(cfg) },
		ResolveProfileID:  resolveProfileID,
		RequireOperator:   cfg.requireOperator,
		OperatorRefusal:   opgate.Refusal,
		PublicationPrompt: func() string { return publicationAgentPrompt(cfg) },
		RunCLI:            func(ctx context.Context, args ...string) ([]byte, error) { return runCLIInProcess(ctx, cfg, args) },
	}
	if doomsdayChat != nil { // only an engine built with the open-ended mode
		doomsdayChat(cfg, &d)
	}
	return d
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

// runCLIInProcess runs one command through a fresh root command, with the database and the profile
// of cfg named explicitly and --json set, and returns what it printed to stdout.
func runCLIInProcess(ctx context.Context, cfg *globalConfig, args []string) ([]byte, error) {
	root := newRootCmd()
	full := []string{"--json"}
	if cfg.DBPath != "" {
		full = append(full, "--db-path", cfg.DBPath)
	}
	if cfg.ProfileID != "" {
		full = append(full, "--profile", cfg.ProfileID)
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append(full, args...))
	root.SilenceUsage = true
	if err := root.ExecuteContext(ctx); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(out.Bytes()), nil
}
