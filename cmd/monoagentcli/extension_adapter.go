package main

import (
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/extensioncli"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// `extension` lives in internal/cli/extensioncli; this is its adapter: the CLI's globals and
// helpers handed over as extensioncli.Deps, and the old names of the bridge helpers the rest of
// the CLI (capture, the daemon, workflow runs, doctor) uses.

func extensionDeps(cfg *globalConfig) extensioncli.Deps {
	return extensioncli.Deps{
		JSONOutput:       func() bool { return cfg.JSONOutput },
		OpenDB:           func() (*storage.Database, error) { return initDB(cfg) },
		ResolveProfileID: resolveProfileID,
		Version:          getVersion,
		ServingCommand:   func(c *cobra.Command) *cobra.Command { return servingCommand(c) },
		Refusal:          opgate.Refusal,
		RecoveryRefusal:  opgate.RecoveryRefusal,
		RecoveryCheck:    opgate.RecoveryCheck,
	}
}

func newExtensionCmd(cfg *globalConfig) *cobra.Command {
	return extensioncli.NewCommand(extensionDeps(cfg))
}

// newExtensionServer builds the extension server every process that owns one runs.
func newExtensionServer(logger zerolog.Logger) *extension.Server {
	return extensioncli.NewServer(logger, getVersion())
}

// bridgeAdvice says how to keep a bridge running.
const bridgeAdvice = extensioncli.BridgeAdvice

// bridgeLifetimeHint is bridgeAdvice, but only for a bridge this process owns.
func bridgeLifetimeHint(bridge connChecker) string { return extensioncli.BridgeLifetimeHint(bridge) }

// captureHooks are the capture summaries and indexing installed into a server.
type captureHooks = extensioncli.CaptureHooks

func installCaptureSummaries(srv *extension.Server, logf func(string, ...any)) *captureHooks {
	return extensioncli.InstallCaptureSummaries(srv, logf)
}

// checkSummaryChoice validates a capture's summary mode, runtime and model.
var checkSummaryChoice = extensioncli.CheckSummaryChoice
