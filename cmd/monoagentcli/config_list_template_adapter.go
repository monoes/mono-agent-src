package main

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/configcli"
	"github.com/monoes/mono-agent/internal/cli/messagetemplate"
	"github.com/monoes/mono-agent/internal/cli/sociallist"
	"github.com/monoes/mono-agent/internal/storage"
)

// `config`, `message-template` and `list` live in internal/cli/configcli, messagetemplate and
// sociallist; these are their adapters: the CLI's globals handed over as each package's Deps.

func newConfigCmd(cfg *globalConfig) *cobra.Command {
	return configcli.NewCommand(configcli.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
	})
}

func newTemplateCmd(cfg *globalConfig) *cobra.Command {
	return messagetemplate.NewCommand(messagetemplate.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
	})
}

func newListCmd(cfg *globalConfig) *cobra.Command {
	return sociallist.NewCommand(sociallist.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
	})
}
