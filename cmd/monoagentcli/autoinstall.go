package main

import (
	"errors"
	"io"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/autoinstall"
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
				return re.res
			}
			return nil
		},
		Reported: func(err error) error { return reportedError{err} },
		NotFound: func(format string, a ...any) error { return errNotFound(format, a...) },
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

// installConfirmer decides whether an install proceeds after its review.
// interactive=false means no prompt is possible.
type installConfirmer struct {
	yes         bool
	interactive bool
	in          io.Reader
	out         io.Writer
}

// runInstall runs the review → confirm → install flow shared by
// `automation install` and `action import`.
func runInstall(opts automation.InstallOptions, c installConfirmer,
	do func(automation.InstallOptions) (*automation.InstallResult, error)) (*automation.InstallResult, error) {
	// RunInstall reads only WithInstallResult.
	return autoinstall.Deps{WithInstallResult: withInstallResult}.RunInstall(opts,
		autoinstall.Confirmer{Yes: c.yes, Interactive: c.interactive, In: c.in, Out: c.out}, do)
}

func printFailedIssues(cmd *cobra.Command, cfg *globalConfig, err error) {
	autoinstallDeps(cfg).PrintFailedIssues(cmd, err)
}

func printInstallResult(out io.Writer, cfg *globalConfig, res *automation.InstallResult) error {
	return autoinstallDeps(cfg).PrintInstallResult(out, res)
}

func printFileResult(out io.Writer, cfg *globalConfig, file, sum, verb string) error {
	return autoinstallDeps(cfg).PrintFileResult(out, file, sum, verb)
}

var (
	writeHashed      = autoinstall.WriteHashed
	issuesHaveErrors = autoinstall.IssuesHaveErrors
)

// exportDomainFlags are --domains / --use-suggested-domains (autoinstall.DomainFlags).
type exportDomainFlags struct{ autoinstall.DomainFlags }

func (f *exportDomainFlags) register(cmd *cobra.Command) { f.Register(cmd) }

func (f *exportDomainFlags) resolve(reg *automation.Registry, id string) ([]string, error) {
	return f.Resolve(reg, id)
}
