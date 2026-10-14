package main

import (
	"github.com/spf13/cobra"

	versioncli "github.com/monoes/mono-agent/internal/cli/version"
)

// `version` lives in internal/cli/version; this is its adapter.

func newVersionCmd() *cobra.Command {
	return versioncli.NewCommand(versioncli.Deps{Version: getVersion, BuildDate: getBuildDate})
}
