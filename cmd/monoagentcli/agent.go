package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agentinstall"
	agentcli "github.com/monoes/mono-agent/internal/cli/agent"
	"github.com/monoes/mono-agent/internal/storage"
)

// `agent` lives in internal/cli/agent; this is its adapter: the CLI's globals and helpers
// handed over as agentcli.Deps. The daemon's re-validation scheduler stays in
// agent_auto_revalidate.go.

// agentBaseDeps is the part of agentDeps that needs no globalConfig: the CLI's errors and
// streamed output, which `doctor fix` shares through installRuntime.
var agentBaseDeps = agentcli.Deps{
	NotFound:        errNotFound,
	InvalidInput:    errInvalidInput,
	NewTable:        newPlainTable,
	ProgressWriter:  progressWriter,
	FinishStreamed:  finishStreamed,
	StdinIsTerminal: stdinIsTerminal,
}

func agentDeps(cfg *globalConfig) agentcli.Deps {
	d := agentBaseDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.OpenDB = func() (*storage.Database, error) { return initDB(cfg) }
	return d
}

// newAgentCmd exposes the local agent-runner surface: thin proxies over
// monomind's Agent Exec Protocol commands.
func newAgentCmd(cfg *globalConfig) *cobra.Command { return agentcli.NewCommand(agentDeps(cfg)) }

// newAgentTestCmd is `agent test` on its own.
func newAgentTestCmd(cfg *globalConfig) *cobra.Command {
	return agentcli.NewTestCommand(agentDeps(cfg))
}

// realRuntimeMachine is the machine installRuntime runs on outside tests.
func realRuntimeMachine() agentcli.RuntimeMachine { return agentcli.RealRuntimeMachine() }

// installRuntime installs one runtime by its `agent scan` id and verifies
// it with a re-scan. confirmScript gates vendor install scripts.
func installRuntime(ctx context.Context, m agentcli.RuntimeMachine, id string, force bool, confirmScript func(agentinstall.Recipe) bool,
	progress func(string)) (string, error) {
	return agentBaseDeps.InstallRuntime(ctx, m, id, force, confirmScript, progress)
}

// shellQuote quotes s for a POSIX shell when it needs it.
func shellQuote(s string) string { return agentcli.ShellQuote(s) }
