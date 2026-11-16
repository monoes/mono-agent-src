package main

import (
	"errors"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/autoinstall"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/spf13/cobra"
)

// The automation install pipeline (validate, pack, install, export and the
// lifecycle commands, with the helpers action, library and workflow bundles
// share) lives in internal/cli/autoinstall. This file hands it the CLI's
// globals and keeps the old names for the rest of the CLI.

// autoinstallDeps hands the CLI's globals and helpers to internal/cli/autoinstall.
func autoinstallDeps(cfg *globalConfig) autoinstall.Deps {
	return autoinstall.Deps{
		JSONOutput:        func() bool { return cfg.JSONOutput },
		OpenRegistry:      openAutomationRegistry,
		StdinIsTerminal:   stdinIsTerminal,
		WithInstallResult: withInstallResult,
		InstallResultOf: func(err error) *automation.InstallResult {
			var re *installResultError
			if errors.As(err, &re) {
				return re.Res
			}
			return nil
		},
		Reported: func(err error) error { return reportedError{Err: err} },
		NotFound: func(format string, a ...any) error { return errNotFound(format, a...) },
		Refusal:  opgate.Refusal,
	}
}

func newAutomationValidateCmd(cfg *globalConfig) *cobra.Command {
	return autoinstallDeps(cfg).NewValidateCmd()
}

func newAutomationPackCmd(cfg *globalConfig) *cobra.Command {
	return autoinstallDeps(cfg).NewPackCmd()
}

func newAutomationInstallCmd(cfg *globalConfig) *cobra.Command {
	return autoinstallDeps(cfg).NewInstallCmd()
}

func newAutomationExportCmd(cfg *globalConfig) *cobra.Command {
	return autoinstallDeps(cfg).NewExportCmd()
}

func newAutomationLifecycleCmds(cfg *globalConfig) []*cobra.Command {
	return autoinstallDeps(cfg).NewLifecycleCmds()
}

// errConfirmationRequired is returned when an install needs a yes/no answer
// that nobody can give: --json, or stdin is not a terminal (contracts §5).
var errConfirmationRequired = autoinstall.ErrConfirmationRequired
