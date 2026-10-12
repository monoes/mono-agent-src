package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Which browser each profile runs in. A browser is one browser profile with
// the MonoAgent Bridge extension; the binding is stored in that extension,
// and these commands read it through the bridge or ask the extension to
// change it.

// browserRow is one connected browser as `extension browsers --json`
// prints it. The desktop app reads these keys.
type browserRow struct {
	Instance string `json:"instance"`
	Label    string `json:"label"`
	// ProfileID/ProfileName are the first profile, for older readers.
	ProfileID   string `json:"profile_id"`
	ProfileName string `json:"profile_name"`
	// ProfileIDs/ProfileNames are every profile this browser runs.
	ProfileIDs   []string `json:"profile_ids"`
	ProfileNames []string `json:"profile_names"`
	Legacy       bool     `json:"legacy"`
	Conflict     bool     `json:"conflict"`
	Version      string   `json:"version"`
	ConnectedAt  string   `json:"connected_at"`
}

type browsersReport struct {
	Running  bool         `json:"running"`
	Hint     string       `json:"hint,omitempty"`
	Browsers []browserRow `json:"browsers"`
}

const noBridgeHint = "No bridge is running. Start one with `monoagentcli extension serve`."

func newExtensionBrowsersCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "browsers",
		Short: "List the browsers attached to the bridge and the profiles each one runs",
		Long: "Every browser profile with the MonoAgent Bridge extension is its own browser here.\n" +
			"A browser bound to profiles runs those profiles' browser actions, logged in as that\n" +
			"browser's accounts; any number of profiles may share one browser. An unbound browser\n" +
			"is the default for profiles with no browser of their own. Bind with `extension bind`,\n" +
			"`extension assign` or in the extension's side panel.",
		Example: "  monoagentcli extension browsers\n  monoagentcli extension browsers --json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runExtensionBrowsers(cmd.Context(), cfg, cmd.OutOrStdout())
		},
	}
}

func newExtensionBindCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "bind <browser> <profile>",
		Short: "Also run a profile's browser actions in this browser",
		Long: "<browser> is an instance id (or its first 4+ characters) or a label from\n" +
			"`extension browsers`; <profile> is a profile id or name. The profile is ADDED to the\n" +
			"profiles the browser already runs (it needs extension 1.8.0 or later to run more than\n" +
			"one); other browsers keep it too. To move a profile to one browser use `extension assign`.\n" +
			"The browser keeps the binding across restarts; it is stored in that browser's extension.",
		Example: "  monoagentcli extension bind \"Edge Work\" Work\n  monoagentcli extension bind 3f2a Personal",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profileID, err := profileIDFor(cfg, args[1])
			if err != nil {
				return err
			}
			return runExtensionBind(cmd.Context(), cfg, cmd.OutOrStdout(), args[0], profileID)
		},
	}
}

func newExtensionUnbindCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "unbind <browser> [profile]",
		Short: "Stop running a profile in this browser, or every profile when none is named",
		Long: "With a profile, only that profile is removed from the browser. Without one, the browser\n" +
			"runs no profile of its own: it becomes a default browser again (for profiles with none).",
		Example: "  monoagentcli extension unbind \"Edge Work\" Work\n  monoagentcli extension unbind \"Edge Work\"",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := ""
			if len(args) == 2 {
				profile = args[1]
			}
			return runExtensionUnbind(cmd.Context(), cfg, cmd.OutOrStdout(), args[0], profile)
		},
	}
}

func runExtensionBrowsers(ctx context.Context, cfg *globalConfig, out io.Writer) error {
	report := browsersReport{Browsers: []browserRow{}}
	_, base, ok := findRunningBridge()
	if !ok {
		report.Hint = noBridgeHint
		return printBrowsers(out, cfg.JSONOutput, report)
	}
	report.Running = true
	infos, err := extension.NewRemoteSender(base).Browsers()
	switch {
	case errors.Is(err, extension.ErrBridgeTooOld):
		report.Hint = err.Error()
	case err != nil:
		return errAuthConnection("listing browsers: %v", err)
	default:
		report.Browsers = browserRows(infos, profileNames(ctx, cfg))
	}
	return printBrowsers(out, cfg.JSONOutput, report)
}

// profileIDFor turns a profile id or name into its id.
func profileIDFor(cfg *globalConfig, idOrName string) (string, error) {
	db, err := initDB(cfg)
	if err != nil {
		return "", fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	return resolveProfileID(db.DB, idOrName)
}

// runExtensionBind adds profileID to the profiles one browser runs.
func runExtensionBind(ctx context.Context, cfg *globalConfig, out io.Writer, query, profileID string) error {
	sender, infos, err := bridgeBrowsers()
	if err != nil {
		return err
	}
	target, err := matchBrowser(infos, query)
	if err != nil {
		return err
	}
	set, err := knownSet(target)
	if err != nil {
		return err
	}
	return applyBrowserSet(ctx, cfg, out, sender, target, addProfile(set, profileID))
}

// runExtensionUnbind removes one profile from a browser, or all of them.
func runExtensionUnbind(ctx context.Context, cfg *globalConfig, out io.Writer, query, profile string) error {
	sender, infos, err := bridgeBrowsers()
	if err != nil {
		return err
	}
	target, err := matchBrowser(infos, query)
	if err != nil {
		return err
	}
	var next []string
	if profile != "" {
		set, err := knownSet(target)
		if err != nil {
			return err
		}
		id, err := profileIDFor(cfg, profile)
		if err != nil {
			// A profile deleted since it was bound is no longer in the
			// database, but the browser still lists it: let it be removed.
			if !slices.Contains(set, profile) {
				return err
			}
			id = profile
		}
		next = removeProfile(set, id)
	}
	return applyBrowserSet(ctx, cfg, out, sender, target, next)
}

// applyBrowserSet stores a browser's new profile set and prints the result.
func applyBrowserSet(ctx context.Context, cfg *globalConfig, out io.Writer, sender *extension.RemoteSender, target extension.ConnInfo, set []string) error {
	if err := setBrowserProfiles(sender, target, set); err != nil {
		return err
	}
	infos, err := sender.Browsers()
	if err != nil {
		return errAuthConnection("listing browsers: %v", err)
	}
	for _, row := range browserRows(infos, profileNames(ctx, cfg)) {
		if row.Instance == target.Instance {
			if cfg.JSONOutput {
				return json.NewEncoder(out).Encode(row)
			}
			printBrowserTable(out, []browserRow{row})
			return nil
		}
	}
	return errNotFound("the browser disconnected right after binding — check `monoagentcli extension browsers`")
}

// matchBrowser finds one browser by instance id, id prefix (4+ chars) or
// label (case-insensitive).
func matchBrowser(infos []extension.ConnInfo, query string) (extension.ConnInfo, error) {
	q := strings.TrimSpace(query)
	var hits []extension.ConnInfo
	for _, b := range infos {
		if b.Instance == q {
			return b, nil
		}
	}
	for _, b := range infos {
		if (len(q) >= 4 && strings.HasPrefix(b.Instance, q)) || strings.EqualFold(b.Label, q) {
			hits = append(hits, b)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return extension.ConnInfo{}, errNotFound("no connected browser matches %q — see `monoagentcli extension browsers`", q)
	default:
		return extension.ConnInfo{}, errInvalidInput("%q matches %d browsers — use more of the id", q, len(hits))
	}
}

func browserRows(infos []extension.ConnInfo, names map[string]string) []browserRow {
	rows := make([]browserRow, 0, len(infos))
	for _, b := range infos {
		ids := b.ProfileSet()
		row := browserRow{
			Instance: b.Instance, Label: b.Label,
			ProfileIDs: append([]string{}, ids...), ProfileNames: make([]string, 0, len(ids)),
			Legacy: b.Legacy, Conflict: b.Conflict, Version: b.Version,
		}
		for _, id := range ids {
			row.ProfileNames = append(row.ProfileNames, names[id])
		}
		if len(ids) > 0 {
			row.ProfileID, row.ProfileName = ids[0], names[ids[0]]
		}
		if !b.ConnectedAt.IsZero() {
			row.ConnectedAt = b.ConnectedAt.Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows
}

// profileNames maps profile ids to names for display; it is best effort.
func profileNames(ctx context.Context, cfg *globalConfig) map[string]string {
	names := map[string]string{}
	db, err := initDB(cfg)
	if err != nil {
		return names
	}
	defer db.Close()
	list, _ := profiledir.List(ctx, db.DB)
	for _, p := range list {
		names[p.ID] = p.Name
	}
	return names
}

func printBrowsers(out io.Writer, asJSON bool, report browsersReport) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	if report.Hint != "" {
		fmt.Fprintln(out, report.Hint)
	}
	if report.Running && len(report.Browsers) == 0 && report.Hint == "" {
		fmt.Fprintln(out, "No browser is attached. Open a browser that has the MonoAgent Bridge extension.")
		return nil
	}
	if len(report.Browsers) > 0 {
		printBrowserTable(out, report.Browsers)
	}
	return nil
}

func printBrowserTable(out io.Writer, rows []browserRow) {
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "BROWSER\tID\tPROFILE\tNOTE")
	for _, r := range rows {
		label := r.Label
		if label == "" {
			label = "(unnamed)"
		}
		id := r.Instance
		if len(id) > 8 {
			id = id[:8]
		}
		profile := "any profile"
		if len(r.ProfileIDs) > 0 {
			shown := make([]string, len(r.ProfileIDs))
			for i, id := range r.ProfileIDs {
				shown[i] = r.ProfileNames[i]
				if shown[i] == "" {
					shown[i] = id + " (unknown profile)"
				}
			}
			profile = strings.Join(shown, ", ")
		}
		note := ""
		switch {
		case r.Legacy:
			note = "extension too old to bind — reload it"
		case r.Conflict:
			note = "another browser runs one of these profiles; the newer one is used"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", label, id, profile, note)
	}
	_ = tw.Flush()
}
