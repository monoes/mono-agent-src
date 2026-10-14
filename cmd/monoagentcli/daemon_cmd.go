package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/cli/daemon"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The workflow-run memory starts with `monoagentcli daemon` itself (internal/cli/daemon), once
// cobra has initialized.
func init() {
	cobra.OnInitialize(func() { daemon.StartRunMemory(os.Args[1:], openWorkflowStore) })
}

// newDaemonCmd is `daemon` (internal/cli/daemon), behind the account gate.
func newDaemonCmd(cfg *globalConfig) *cobra.Command {
	return servingCommand(daemon.NewCommand(daemonDeps(cfg)))
}

// newDaemonRestartCmd is `daemon restart` on its own, for the tests.
func newDaemonRestartCmd(cfg *globalConfig) *cobra.Command {
	return daemon.NewRestartCommand(daemonDeps(cfg))
}

// daemonDeps are the CLI's pieces the daemon runs on.
func daemonDeps(cfg *globalConfig) daemon.Deps {
	return daemon.Deps{
		ProfileID:             func() string { return cfg.ProfileID },
		JSONOutput:            func() bool { return cfg.JSONOutput },
		OpenDB:                func() (*storage.Database, error) { return initDB(cfg) },
		BuildEngine:           func() (*workflow.WorkflowEngine, func(), error) { return buildEngine(cfg, true) },
		OpenWorkflowStore:     openWorkflowStore,
		API:                   apiRuntimeDeps(),
		NewExtensionServer:    newExtensionServer,
		ProbeAddrs:            extensionProbeAddrs,
		InstallCaptureHooks:   installCaptureSummaries,
		StartAutoRevalidation: startAutoRevalidation,
		// newInstaller is read when the command runs: tests replace it.
		NewInstaller: func() autostart.Installer { return newInstaller() },
		WriteJSON:    writeJSONTo,
	}
}
