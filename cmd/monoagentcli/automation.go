package main

import (
	"io"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	automationcli "github.com/monoes/mono-agent/internal/cli/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// newAutomationCmd builds `monoagentcli automation …` (browser automation
// packages). Owned by the cli builder; see the build contracts doc.
// JSON shapes are fixed by contracts §5 — the GUI parses them as-is.
func newAutomationCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "automation",
		Short: "Manage browser automation packages",
		Long: `Browser automation packages: a manifest, actions, fragments, selectors and
optional page scripts for one site, installed under ~/.monoagent/automations.
The official packages (Instagram, LinkedIn, X, TikTok, Hacker News, Product
Hunt, Gemini) are published on monoes.me: log in with
` + "`monoagentcli library login`" + `, then install them with
` + "`monoagentcli library install automation <id>`" + `. Anything else is installed
from a .mpkg file, a package directory or a URL. Packages an earlier version
seeded stay installed.`,
	}
	// list, show, new, test, doctor, trust and rerecord live in internal/cli/automation.
	cmd.AddCommand(automationcli.Commands(automationDeps(cfg))...)
	cmd.AddCommand(
		newAutomationValidateCmd(cfg),
		newAutomationPackCmd(cfg),
		newAutomationInstallCmd(cfg),
		newAutomationExportCmd(cfg),
	)
	cmd.AddCommand(newAutomationLifecycleCmds(cfg)...)
	for _, sub := range cmd.Commands() {
		withJSONErrors(cfg, sub)
	}
	return cmd
}

// automationDeps hands the CLI's globals and helpers to internal/cli/automation.
func automationDeps(cfg *globalConfig) automationcli.Deps {
	return automationcli.Deps{
		JSONOutput:   func() bool { return cfg.JSONOutput },
		ProfileID:    func() string { return cfg.ProfileID },
		Verbose:      func() bool { return cfg.Verbose },
		DBPath:       func() string { return expandPath(cfg.DBPath) },
		OpenDB:       func() (*storage.Database, error) { return initDB(cfg) },
		OpenRegistry: openAutomationRegistry,
		NewTable:     newPlainTable,
		WithJSONErrors: func(cmd *cobra.Command) {
			withJSONErrors(cfg, cmd)
		},
		Reported:          func(err error) error { return reportedError{Err: err} },
		WithInstallResult: withInstallResult,
		SetupBridge:       setupProfileBridge,
		EnsureConnected: func(bridge browserpkg.ExtensionBridge, timeout time.Duration) error {
			return ensureExtensionConnected(bridge, timeout)
		},
		Refusal: opgate.Refusal,
	}
}

// printIssues prints validation issues (validate and install share it with the package).
var printIssues = automationcli.PrintIssues

// withJSONErrors makes a failing command also print {"error": "…"} on
// stdout when --json is set (clicore.WithJSONErrors).
func withJSONErrors(cfg *globalConfig, cmd *cobra.Command) {
	clicore.WithJSONErrors(func() bool { return cfg.JSONOutput }, cmd)
}

// jsonErrorFields is implemented by errors that carry machine-readable
// detail for `--json`.
type jsonErrorFields = clicore.JSONErrorFields

// jsonErrorCode names a classified error for {"error","code"} (clicore.JSONErrorCode).
func jsonErrorCode(err error) string { return clicore.JSONErrorCode(err) }

// reportedError is a failure whose details the command already printed.
type reportedError = clicore.Reported

// installResultError is an Install/AddAction failure that still carries the result.
type installResultError = clicore.InstallResultError

// withInstallResult wraps err so the failed result is not lost.
func withInstallResult(err error, res *automation.InstallResult) error {
	return clicore.WithInstallResult(err, res)
}

// openAutomationRegistry opens the default registry (clicore.OpenAutomationRegistry).
func openAutomationRegistry() (*automation.Registry, error) {
	return clicore.OpenAutomationRegistry(version)
}

// writeJSONTo writes v as indented JSON to w.
func writeJSONTo(w io.Writer, v any) error { return clicore.WriteJSONTo(w, v) }
