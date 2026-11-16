package main

import (
	"fmt"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/extension"
)

// browser browsers · tabs · open · navigate · reload · close

type browserRow struct {
	Instance    string `json:"instance"`
	Label       string `json:"label"`
	Profile     string `json:"profile,omitempty"`
	Version     string `json:"version,omitempty"`
	Legacy      bool   `json:"legacy,omitempty"`
	Conflict    bool   `json:"conflict,omitempty"`
	ConnectedAt string `json:"connectedAt,omitempty"`
	// Outdated is set when the extension is older than extension.MinExtension; Warning says how to update it.
	Outdated bool   `json:"outdated"`
	Warning  string `json:"warning,omitempty"`
}

// browserRows turns the bridge's connections into `browser browsers` rows.
func browserRows(infos []extension.ConnInfo) []browserRow {
	rows := []browserRow{}
	for _, b := range infos {
		row := browserRow{Instance: b.Instance, Label: b.Label, Profile: b.Profile, Version: b.Version, Legacy: b.Legacy, Conflict: b.Conflict,
			Outdated: extension.ExtensionOutdated(b.Version), Warning: extension.ExtensionUpdateNote(b.Version)}
		if !b.ConnectedAt.IsZero() {
			row.ConnectedAt = b.ConnectedAt.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows
}

func newBrowserBrowsersCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "browsers",
		Short:   "List the browsers attached to the bridge (for --browser)",
		Example: "  monoagentcli --json browser browsers",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if c := browserctl.AgentCaller(); c.Kind != "" {
				// Machine-wide, but still the browser: refused at level off.
				off := false
				if err := withPermissions(cfg, nil, func(p browserctl.Permissions) error {
					off = p.LevelOf(c) == browserctl.LevelOff
					return nil
				}); err != nil {
					return err
				}
				if off {
					return browserErr(cmd, cfg, &browserctl.BrowserOffError{Kind: c.Kind, Declared: c.Declared})
				}
			}
			sender, err := browserBridge()
			if err != nil {
				return browserErr(cmd, cfg, err)
			}
			infos, err := sender.Browsers()
			if err != nil {
				return browserErr(cmd, cfg, err)
			}
			rows := browserRows(infos)
			return browserOut(cmd, cfg, map[string]any{"browsers": rows}, func() {
				if len(rows) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "No browser is attached. Open a browser that has the MonoAgent Bridge extension.")
					return
				}
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tLABEL\tPROFILE\tNOTE")
				for _, r := range rows {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Instance, r.Label, r.Profile, r.Warning)
				}
				_ = tw.Flush()
			})
		},
	}
}

func newBrowserTabsCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:   "tabs",
		Short: "List the browser's tabs (an agent sees its own group's and allowed pages' tabs)",
		Example: `  monoagentcli --profile <id> browser tabs
  monoagentcli --profile <id> --json browser tabs --browser "Edge Work"`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := openBrowser(cmd, cfg, true)
			if err != nil {
				return err
			}
			defer r.Close()
			tabs, err := r.s.VisibleTabs()
			if err != nil {
				return r.err(err)
			}
			return r.out(map[string]any{"browser": r.s.Browser().Instance, "tabs": tabs}, func() {
				tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
				fmt.Fprintln(tw, "TAB\tACTIVE\tGROUP\tTITLE\tURL")
				for _, t := range tabs {
					active := ""
					if t.Active {
						active = "*"
					}
					fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", t.TabID, active, t.GroupTitle, browserCut(t.Title, 40), browserCut(t.URL, 80))
				}
				_ = tw.Flush()
			})
		},
	}
}

// tabResult is what open, navigate and reload print.
type tabResult struct {
	TabID int    `json:"tabId"`
	URL   string `json:"url"`
}

func (r *browserRun) printTab(verb string) error {
	res := tabResult{TabID: r.s.TabID(), URL: r.s.URL()}
	return r.out(res, func() { fmt.Fprintf(r.cmd.OutOrStdout(), "%s tab %d: %s\n", verb, res.TabID, res.URL) })
}

func newBrowserOpenCmd(cfg *globalConfig) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "open <url>",
		Short: "Open a URL in a new tab, which later commands act on",
		Long: "Open a URL in a new tab and make it this context's tab. An agent's tabs go into its\n" +
			"own tab group (\"Coder · <title>\") and their console and network are recorded from\n" +
			"the first load (the page is loaded again once recording is on).",
		Example: `  monoagentcli --profile <id> --json browser open http://localhost:3000
  monoagentcli --profile <id> browser open https://example.com --group Research`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := openBrowser(cmd, cfg, true)
			if err != nil {
				return err
			}
			defer r.Close()
			if _, err := r.s.OpenTab(args[0], group); err != nil {
				return r.err(err)
			}
			if err := r.s.BeforeAct(cmd.Context(), r.db.DB); err != nil {
				return r.err(err)
			}
			return r.printTab("Opened")
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "Put the tab into the tab group with this title (an agent's default: its own group)")
	return cmd
}

func newBrowserNavigateCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "navigate <url>",
		Short:   "Load a URL in the tab",
		Example: "  monoagentcli --profile <id> browser navigate http://localhost:3000/settings",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := openBrowser(cmd, cfg, false)
			if err != nil {
				return err
			}
			defer r.Close()
			if err := r.act(func() error { _, err := r.s.Navigate(args[0]); return err }); err != nil {
				return err
			}
			if err := r.s.BeforeAct(cmd.Context(), r.db.DB); err != nil {
				return r.err(err)
			}
			return r.printTab("Loaded")
		},
	}
}

func newBrowserReloadCmd(cfg *globalConfig) *cobra.Command {
	var hard bool
	cmd := &cobra.Command{
		Use:     "reload",
		Short:   "Reload the tab",
		Example: "  monoagentcli --profile <id> browser reload --hard",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := openBrowser(cmd, cfg, false)
			if err != nil {
				return err
			}
			defer r.Close()
			if err := r.act(func() error { return r.s.Reload(hard) }); err != nil {
				return err
			}
			return r.printTab("Reloaded")
		},
	}
	cmd.Flags().BoolVar(&hard, "hard", false, "Bypass the cache")
	return cmd
}

func newBrowserCloseCmd(cfg *globalConfig) *cobra.Command {
	return &cobra.Command{
		Use:     "close",
		Short:   "Close the tab",
		Example: "  monoagentcli --profile <id> browser close --tab 1234",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			r, err := openBrowser(cmd, cfg, false)
			if err != nil {
				return err
			}
			defer r.Close()
			id := r.s.TabID()
			if err := r.s.CloseTab(); err != nil {
				return r.err(err)
			}
			return r.out(map[string]any{"tabId": id, "closed": true}, func() {
				fmt.Fprintf(cmd.OutOrStdout(), "Closed tab %s\n", strconv.Itoa(id))
			})
		},
	}
}
