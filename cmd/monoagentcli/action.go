package main

import (
	"github.com/spf13/cobra"

	actioncli "github.com/monoes/mono-agent/internal/cli/action"
)

// newActionCmd is `monoagentcli action`, which lives in internal/cli/action.
func newActionCmd(cfg *globalConfig) *cobra.Command {
	return actioncli.NewCommand(actioncli.Deps{
		Install:        autoinstallDeps(cfg),
		WithJSONErrors: func(cmd *cobra.Command) { withJSONErrors(cfg, cmd) },
		RenderHTML:     renderHTML,
	})
}
