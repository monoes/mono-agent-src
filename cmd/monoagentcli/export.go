package main

import (
	"github.com/spf13/cobra"

	exportcli "github.com/monoes/mono-agent/internal/cli/export"
	"github.com/monoes/mono-agent/internal/storage"
)

// `export` lives in internal/cli/export; this is its adapter: the CLI's globals and helpers
// handed over as exportcli.Deps.

func newExportCmd(cfg *globalConfig) *cobra.Command {
	return exportcli.NewCommand(exportcli.Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OutputDir:  func() string { return cfg.OutputDir },
		OpenDB:     func() (*storage.Database, error) { return initDB(cfg) },
	})
}

// exportPeopleData writes the profile's people to people_export.json (people tests share it).
var exportPeopleData = exportcli.ExportPeople
