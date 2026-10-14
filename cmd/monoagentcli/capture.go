package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	capturecli "github.com/monoes/mono-agent/internal/cli/capture"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
)

// `capture` lives in internal/cli/capture; this is its adapter: the CLI's globals and helpers
// handed over as capturecli.Deps. `capture page` (below: the extension bridge) and
// `capture task` (capture_task.go: the org boards) stay here and are added to the group.

// captureErrDeps is the part of captureDeps that needs no globalConfig: the CLI's errors.
var captureErrDeps = capturecli.Deps{NotFound: errNotFound, InvalidInput: errInvalidInput}

func captureDeps(cfg *globalConfig) capturecli.Deps {
	d := captureErrDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.ProfileID = func() string { return cfg.ProfileID }
	d.DBPath = func() string { return cfg.DBPath }
	d.OpenDB = func() (*storage.Database, error) { return initDB(cfg) }
	d.NewTable = func(w io.Writer, headers []string) *tablewriter.Table {
		return newPlainTable(w, headers, nil)
	}
	return d
}

// newCaptureCmd returns the `capture` command group: save a page from the
// user's real, logged-in Chrome into the monomind inbox
// (~/.monomind/inbox/<timestamp>-<slug>/), and list what is already there.
//
// The capture itself happens in the browser — the extension holds the
// `debugger` permission, so an MHTML snapshot and a print-to-PDF of a page
// the user is signed into need no new permission and no second, cookie-less
// browser. This side sends the command, reassembles what comes back (large
// artifacts arrive in chunks), and writes the envelope. See
// docs/BROWSER_TRACK_PLAN.md and internal/capture.
func newCaptureCmd(cfg *globalConfig) *cobra.Command {
	cmd := capturecli.NewCommand(captureDeps(cfg))
	cmd.AddCommand(newCapturePageCmd(cfg), newCaptureTaskCmd(cfg))
	return cmd
}

// defaultDBPath mirrors the --db-path flag default in root.go (see capturecli.DefaultDBPath).
const defaultDBPath = capturecli.DefaultDBPath

// The capture helpers the extension bridge, record, crawl and people commands share.
var (
	openProfileDB          = capturecli.OpenProfileDB
	extensionProfileSource = capturecli.ExtensionProfileSource
	newCaptureClassifier   = capturecli.NewClassifier
	captureHumanBytes      = capturecli.HumanBytes
	truncateCaptureCell    = capturecli.TruncateCell
	slicesContains         = capturecli.SlicesContains
)

// resolveCaptureProfile turns what the user typed after --profile into a profile and its inbox.
func resolveCaptureProfile(db *sql.DB, idOrName string) (capturecli.Profile, error) {
	return captureErrDeps.ResolveProfile(db, idOrName)
}

// knownCaptureFormats are the artifact kinds the extension can produce.
var knownCaptureFormats = []string{"mhtml", "pdf", "readable", "screenshot"}

// captureModes mirrors chrome-extension/capture_modes.js MODES.
var captureModes = []string{"full", "screenshot", "summary", "video"}

func newCapturePageCmd(cfg *globalConfig) *cobra.Command {
	var (
		tab        int
		formats    []string
		selection  bool
		note       string
		tags       []string
		collection string
		out        string
		mode       string
		sumRuntime string
		sumModel   string
		timeout    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "page",
		Short: "Capture a browser tab into the inbox",
		Long: "Captures a tab in your real Chrome — cookies, session and all — and writes\n" +
			"the capture envelope (page.mhtml, page.pdf, readable.md, screenshot.png and\n" +
			"meta.json) into ~/.monomind/inbox/<timestamp>-<slug>/.\n" +
			"\n" +
			"With no --tab, the active tab is captured. Formats the page cannot produce\n" +
			"are reported as warnings and simply left out of the envelope; the capture\n" +
			"still lands.",
		Example: "  monoagentcli capture page\n" +
			"  monoagentcli capture page --formats mhtml,readable --note \"for the Q4 memo\" --tag research\n" +
			"  monoagentcli capture page --tab 42 --selection --collection reading --json\n" +
			"  monoagentcli capture page --mode video   # a YouTube tab: transcript + AI summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			if tab < 0 {
				return errInvalidInput("--tab must be a positive Chrome tab id, got %d", tab)
			}
			formats, err := normalizeCaptureFormats(formats)
			if err != nil {
				return err
			}
			if mode != "" && !slices.Contains(captureModes, mode) {
				return errInvalidInput("--mode must be one of %s, got %q", strings.Join(captureModes, ", "), mode)
			}
			if err := checkSummaryChoice(mode, sumRuntime, sumModel); err != nil {
				return err
			}

			bridge := setupProfileBridge(browserProfile(cfg), newExtensionBridgeLogger(), 3*time.Second)
			capturer, ok := bridge.(extension.Capturer)
			if !ok {
				return errAuthConnection("this extension bridge cannot capture pages (%T)%s", bridge, bridgeAdvice)
			}
			if err := ensureExtensionConnected(bridge, 30*time.Second); err != nil {
				return errAuthConnection("%v", err)
			}

			res, err := capturer.CapturePage(extension.CaptureRequest{
				TabID:          tab,
				Formats:        formats,
				Selection:      selection,
				Note:           note,
				Tags:           tags,
				Collection:     collection,
				Mode:           mode,
				SummaryRuntime: sumRuntime,
				SummaryModel:   sumModel,
				Timeout:        timeout,
				Inbox:          expandPath(out),
			})
			if err != nil {
				// A capture that fails with the extension gone is almost
				// always the extension going away mid-capture (Chrome
				// suspends an idle service worker), not a bad page — and
				// the bare timeout that used to come back said nothing
				// about either. Exit code 4, not 1: this is a connection
				// failure.
				if !bridge.IsConnected() {
					return errAuthConnection("capturing page: %v\n\nThe browser extension is no longer connected to this process.%s",
						err, bridgeLifetimeHint(bridge))
				}
				return fmt.Errorf("capturing page: %w", err)
			}

			if cfg.JSONOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(res)
			}
			for _, w := range res.Warnings {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", w)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Captured %s (%s) — %s\n",
				res.Meta.DedupeURL(), strings.Join(res.Artifacts, ", "), captureHumanBytes(res.Bytes))
			fmt.Fprintln(cmd.OutOrStdout(), res.Path)
			return nil
		},
	}
	cmd.Flags().IntVar(&tab, "tab", 0, "Chrome tab id to capture (default: the active tab)")
	cmd.Flags().StringSliceVar(&formats, "formats", nil,
		"Artifacts to capture: "+strings.Join(knownCaptureFormats, ", ")+" (default: all of them)")
	cmd.Flags().BoolVar(&selection, "selection", false, "Capture only the current selection, not the whole page")
	cmd.Flags().StringVar(&note, "note", "", "Note to store alongside the capture")
	cmd.Flags().StringSliceVar(&tags, "tag", nil, "Tag to store alongside the capture (repeatable)")
	cmd.Flags().StringVar(&collection, "collection", "", "Collection to file the capture under")
	cmd.Flags().StringVar(&out, "out", "", "Inbox directory to write into (default: ~/.monomind/inbox)")
	cmd.Flags().StringVar(&mode, "mode", "",
		"Capture mode, as in the extension's right-click menu: "+strings.Join(captureModes, ", ")+
			" (summary and video also get an AI summary.md from the bridge)")
	cmd.Flags().StringVar(&sumRuntime, "summary-runtime", "",
		"With --mode summary|video: the agent runtime that writes the summary (default: the bridge's)")
	cmd.Flags().StringVar(&sumModel, "summary-model", "",
		"With --mode summary|video: the model passed to that runtime (default: the runtime's own)")
	cmd.Flags().DurationVar(&timeout, "timeout", extension.DefaultCaptureTimeout,
		"How long to wait for the browser to finish the capture")
	return cmd
}

// normalizeCaptureFormats validates --formats and drops duplicates, so a
// typo fails here with exit code 3 rather than reaching the browser and
// coming back as a capture that is quietly missing an artifact.
func normalizeCaptureFormats(formats []string) ([]string, error) {
	if len(formats) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(formats))
	out := make([]string, 0, len(formats))
	for _, raw := range formats {
		f := strings.ToLower(strings.TrimSpace(raw))
		if f == "" {
			continue
		}
		if !slicesContains(knownCaptureFormats, f) {
			return nil, errInvalidInput("unknown --formats value %q; want one of: %s",
				raw, strings.Join(knownCaptureFormats, ", "))
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, errInvalidInput("--formats was given but named no formats")
	}
	sort.Strings(out)
	return out, nil
}
