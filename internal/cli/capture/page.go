package capturecli

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/extension"
)

// knownCaptureFormats are the artifact kinds the extension can produce.
var knownCaptureFormats = []string{"mhtml", "pdf", "readable", "screenshot"}

// captureModes mirrors chrome-extension/capture_modes.js MODES.
var captureModes = []string{"full", "screenshot", "summary", "video"}

func (d Deps) newCapturePageCmd() *cobra.Command {
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
				return d.InvalidInput("--tab must be a positive Chrome tab id, got %d", tab)
			}
			formats, err := d.normalizeCaptureFormats(formats)
			if err != nil {
				return err
			}
			if mode != "" && !slices.Contains(captureModes, mode) {
				return d.InvalidInput("--mode must be one of %s, got %q", strings.Join(captureModes, ", "), mode)
			}
			if err := d.Page.CheckSummaryChoice(mode, sumRuntime, sumModel); err != nil {
				return err
			}

			bridge := d.Page.Bridge()
			capturer, ok := bridge.(extension.Capturer)
			if !ok {
				return d.AuthConnection("this extension bridge cannot capture pages (%T)%s", bridge, d.Page.Advice)
			}
			if err := d.Page.EnsureConnected(bridge, 30*time.Second); err != nil {
				return d.AuthConnection("%v", err)
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
					return d.AuthConnection("capturing page: %v\n\nThe browser extension is no longer connected to this process.%s",
						err, d.Page.LifetimeHint(bridge))
				}
				return fmt.Errorf("capturing page: %w", err)
			}

			if d.JSONOutput() {
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
func (d Deps) normalizeCaptureFormats(formats []string) ([]string, error) {
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
			return nil, d.InvalidInput("unknown --formats value %q; want one of: %s",
				raw, strings.Join(knownCaptureFormats, ", "))
		}
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil, d.InvalidInput("--formats was given but named no formats")
	}
	sort.Strings(out)
	return out, nil
}
