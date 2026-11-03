package main

import (
	"github.com/spf13/cobra"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/operator"
)

// doomsdayEngine is the store/stop adapter of the open-ended mode. It is nil
// on every install until the mode is built in: nil means doomsdaycli.Off, which
// refuses to start anything. Nothing reads a setting or an environment
// variable to turn the mode on; wiring an engine is a code change.
var doomsdayEngine func(*globalConfig) doomsdaycli.Engine

func newDoomsdayCmd(cfg *globalConfig) *cobra.Command {
	// The gate is the same requireOperator every operator-only verb uses, handed
	// over as a value; doomsday_gate_test.go holds this verb's own table of it.
	gate := cfg.requireOperator
	return doomsdaycli.NewCommand(doomsdaycli.Deps{
		Engine: func() doomsdaycli.Engine {
			if doomsdayEngine == nil {
				return nil
			}
			return doomsdayEngine(cfg)
		},
		RequireOperator: gate,
		OperatorRefusal: operator.Refusal,
		StdinIsTerminal: stdinIsTerminal,
		JSONOutput:      func() bool { return cfg.JSONOutput },
	})
}
