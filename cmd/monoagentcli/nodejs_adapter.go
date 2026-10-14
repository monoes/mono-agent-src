package main

import (
	"io"

	"github.com/spf13/cobra"

	nodejscli "github.com/monoes/mono-agent/internal/cli/nodejs"
)

// `nodejs` lives in internal/cli/nodejs; this is its adapter.

func newNodejsCmd(cfg *globalConfig) *cobra.Command {
	return nodejscli.NewCommand(nodejscli.Deps{
		JSONOutput:      func() bool { return cfg.JSONOutput },
		StdinIsTerminal: stdinIsTerminal,
	})
}

// finishStreamed ends a command whose progress was streamed with progressWriter
// (nodejscli.FinishStreamed).
func finishStreamed(out io.Writer, asJSON bool, err error, okMsg string) error {
	return nodejscli.FinishStreamed(out, asJSON, err, okMsg)
}
