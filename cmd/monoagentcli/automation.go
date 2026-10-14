package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	automationcli "github.com/monoes/mono-agent/internal/cli/automation"
	"github.com/monoes/mono-agent/internal/monomind"
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
		Reported:          func(err error) error { return reportedError{err} },
		WithInstallResult: withInstallResult,
		SetupBridge:       setupProfileBridge,
		EnsureConnected: func(bridge browserpkg.ExtensionBridge, timeout time.Duration) error {
			return ensureExtensionConnected(bridge, timeout)
		},
	}
}

// printIssues prints validation issues (validate and install share it with the package).
var printIssues = automationcli.PrintIssues

// orDash is s, or "-" when s is empty.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// withJSONErrors makes a failing command also print {"error": "…"} on
// stdout when --json is set (contracts §5), with the extra fields of an
// install result. main still prints the error to stderr and exits non-zero.
func withJSONErrors(cfg *globalConfig, cmd *cobra.Command) {
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := run(c, args)
		var rep reportedError
		if err != nil && cfg.JSONOutput && !errors.As(err, &rep) {
			body := map[string]any{"error": err.Error()}
			if code := jsonErrorCode(err); code != "" {
				body["code"] = code
			}
			var re *installResultError
			if errors.As(err, &re) {
				body["issues"], body["result"] = re.res.Issues, re.res
			}
			// An error that knows its machine-readable form (a code, the
			// keys involved) adds it, so callers need not match the text.
			var fields jsonErrorFields
			if errors.As(err, &fields) {
				for k, v := range fields.JSONErrorFields() {
					if k != "error" {
						body[k] = v
					}
				}
			}
			b, _ := json.Marshal(body)
			fmt.Fprintln(c.OutOrStdout(), string(b))
			// Printed: main adds no second {"error"} for a reportedError.
			return reportedError{err}
		}
		return err
	}
}

// jsonErrorFields is implemented by errors that carry machine-readable
// detail for `--json` (e.g. recordanalyze.SelectorConflictError: code
// "selector_conflict" and the conflicting keys).
type jsonErrorFields interface {
	JSONErrorFields() map[string]any
}

// jsonErrorCode names a classified error for {"error","code"}:
// agent_not_setup (the AI agent is not installed or not logged in; its
// exit code is unchanged), else its exit-code class — not_found (2),
// invalid_input (3), auth_or_connection (4); "" for a plain error (exit 1),
// which prints {"error"} alone.
func jsonErrorCode(err error) string {
	if monomind.IsAgentNotSetup(err) {
		return monomind.AgentNotSetupCode
	}
	switch exitCodeFor(err) {
	case 2:
		return "not_found"
	case 3:
		return "invalid_input"
	case 4:
		return "auth_or_connection"
	}
	return ""
}

// reportedError is a failure whose details the command already printed
// (e.g. `automation test` results): it sets the exit code, and --json
// prints no extra {"error"} object after the output.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// installResultError is an Install/AddAction failure that still carries the
// result (review + issues). With --json the error object adds "issues" and
// "result"; without, the issues are printed on stderr.
type installResultError struct {
	err error
	res *automation.InstallResult
}

func (e *installResultError) Error() string { return e.err.Error() }
func (e *installResultError) Unwrap() error { return e.err }

// withInstallResult wraps err so the failed result is not lost.
func withInstallResult(err error, res *automation.InstallResult) error {
	if err == nil || res == nil {
		return err
	}
	return &installResultError{err: err, res: res}
}

// openAutomationRegistry opens the default registry and folds any new
// legacy ~/.monoagent/actions/<p> directories into local-<p> packages.
// Every automation subcommand starts here. Nothing is seeded: the official
// packages come from monoes.me (`monoagentcli library install automation
// <id>`), and packages installed by earlier versions stay as they are.
func openAutomationRegistry() (*automation.Registry, error) {
	if version != "" { // release build: enforce manifests' "engine" ranges
		automation.EngineVersion = version
	}
	reg, err := automation.Default()
	if err != nil {
		return nil, fmt.Errorf("open automation registry: %w", err)
	}
	rep, err := reg.Boot()
	if err != nil {
		return nil, fmt.Errorf("fold legacy actions: %w", err)
	}
	for _, id := range rep.LegacyWrapped {
		stderrf("note: wrapped legacy actions into automation package %s (~/.monoagent/actions is left as is)\n", id)
	}
	return reg, nil
}

// writeJSONTo writes v as indented JSON to w.
func writeJSONTo(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// confirmYes asks a y/N question on in; anything but y/yes is a no.
func confirmYes(in io.Reader, out io.Writer, question string) bool {
	fmt.Fprintf(out, "%s [y/N]: ", question)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// splitCSV splits a comma-separated flag value, dropping empty items.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// stderrf prints a note on stderr (kept off stdout so --json stays parseable).
func stderrf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format, a...)
}
