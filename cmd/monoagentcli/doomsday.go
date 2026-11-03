package main

import (
	"github.com/spf13/cobra"

	chatcli "github.com/monoes/mono-agent/internal/cli/chat"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// doomsdayEngine is the store/stop adapter of the open-ended mode. It is nil
// on every install until the mode is built in: nil means doomsdaycli.Off, which
// refuses to start anything. Nothing reads a setting or an environment
// variable to turn the mode on; wiring an engine is a code change.
var doomsdayEngine func(*globalConfig) doomsdaycli.Engine

// doomsdayChat is set by the same build: it hands the chat commands the resolver of the open run's merge gate
// (chatcli.Deps.DoomsdayMerge) and installs the worktree reconcile guard. Nil leaves merges and reconcile as they were.
// doomsdayStrategyReader is set by the same build: the read-only reader of the `doomsday strategy` verbs. Nil leaves
// them printing "off".
var doomsdayStrategyReader func(*globalConfig) doomsdaycli.StrategyReader

var doomsdayChat func(cfg *globalConfig, d *chatcli.Deps)

func newDoomsdayCmd(cfg *globalConfig) *cobra.Command {
	// Both questions are the turn-aware ones every operator-only verb asks: requireOperator, and opgate.Refusal
	// (agent tokens, markers, a coder session's turn). doomsday_gate_test.go and the rows of
	// operatorOnlyRows hold them.
	gate := cfg.requireOperator
	return doomsdaycli.NewCommand(doomsdaycli.Deps{
		Engine: func() doomsdaycli.Engine {
			if doomsdayEngine == nil {
				return nil
			}
			return doomsdayEngine(cfg)
		},
		RequireOperator: gate,
		OperatorRefusal: opgate.Refusal,
		StdinIsTerminal: stdinIsTerminal,
		JSONOutput:      func() bool { return cfg.JSONOutput },
		StrategyReader: func() doomsdaycli.StrategyReader {
			if doomsdayStrategyReader == nil {
				return nil
			}
			return doomsdayStrategyReader(cfg)
		},
	})
}
