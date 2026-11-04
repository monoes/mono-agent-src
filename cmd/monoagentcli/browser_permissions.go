package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// browser permissions — what each kind of agent may do in the browser and
// the per-site rules (plan C8). Machine-wide; the GUI's Settings section
// shows and changes it through these commands. Only the user changes it.

// permissionsDoc is `browser permissions --json`.
type permissionsDoc struct {
	Levels  map[string]string      `json:"levels"`
	Sites   []browserctl.SiteRule  `json:"sites"`
	Kinds   []permissionsKind      `json:"kinds"`
	Catalog []browserctl.LevelInfo `json:"catalog"`
	You     permissionsYou         `json:"you"`
}

type permissionsKind struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Level     string `json:"level"`
	IsDefault bool   `json:"isDefault"`
}

// permissionsYou is the caller: an agent's kind and level, or "human" and
// "unrestricted" for the user's own terminal. Declared is a kind the agent
// claimed but could not prove: the level is then the stricter of that
// kind's and its own.
type permissionsYou struct {
	Kind     string `json:"kind"`
	Level    string `json:"level"`
	Declared string `json:"declared,omitempty"`
}

func newPermissionsDoc(p browserctl.Permissions) permissionsDoc {
	doc := permissionsDoc{Levels: map[string]string{}, Sites: p.Sites, Catalog: browserctl.Catalog()}
	if doc.Sites == nil {
		doc.Sites = []browserctl.SiteRule{}
	}
	for _, k := range browserctl.Kinds {
		l := p.Level(k)
		doc.Levels[k] = l
		doc.Kinds = append(doc.Kinds, permissionsKind{Kind: k, Label: browserctl.KindLabel(k), Level: l, IsDefault: l == browserctl.DefaultLevels[k]})
	}
	doc.You = permissionsYou{Kind: "human", Level: "unrestricted"}
	if c := browserctl.AgentCaller(); c.Kind != "" {
		doc.You = permissionsYou{Kind: c.Kind, Level: p.LevelOf(c), Declared: c.Declared}
	}
	return doc
}

func newBrowserPermissionsCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "permissions",
		Short: "What each kind of agent may do in the browser, and the site rules",
		Long: "Each kind of agent — coder chats, org roles, the in-app chat, other agents (a Claude Code or\n" +
			"Codex session in a terminal) — has a level:\n" +
			"  full-no-approvals  any site; form submits go through without asking, and scripts run on\n" +
			"                     any page (they can read that site's cookies and storage)\n" +
			"  full               any site; a form submit on a real site waits for your approval\n" +
			"  dev                localhost and dev pages plus allowed sites (the default but for the chat)\n" +
			"  ask                like dev, but a new site asks you first instead of being refused\n" +
			"  off                no browser at all\n" +
			"Site rules apply to every kind: allow (for dev and ask) admits exactly one origin\n" +
			"(scheme, host and port; *.host for a host and its subdomains); block covers a host and its\n" +
			"subdomains on every scheme and port, and wins at every level.\n" +
			"Your own terminal is never restricted. Agents may read this; only you change it.",
		Example: `  monoagentcli --json browser permissions
  monoagentcli browser permissions set coder ask
  monoagentcli browser permissions site https://github.com allow
  monoagentcli browser permissions site https://bank.example block`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withPermissions(cfg, nil, func(p browserctl.Permissions) error { return printPermissions(cmd, cfg, p) })
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "set <kind|all> <level>",
		Short:   "Set a kind of agent's level (coder, apply, org, chat, other), or every kind's with all",
		Example: "  monoagentcli browser permissions set chat dev\n  monoagentcli browser permissions set all full",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := opgate.Refusal(); why != "" {
				return operatorOnlyError("%s: an agent may not change browser permissions: ask the user to run `monoagentcli browser permissions set %s %s` or use Settings → Agent browser permissions", why, args[0], args[1])
			}
			kinds := []string{args[0]}
			if args[0] == "all" {
				kinds = browserctl.Kinds
			}
			return withPermissions(cfg, func(p *browserctl.Permissions) error {
				for _, k := range kinds {
					if args[0] == "all" && k == browserctl.KindApply && args[1] == browserctl.LevelFullNoApprovals {
						continue // apply never gets it; `all` leaves apply as it is
					}
					if err := p.SetLevel(k, args[1]); err != nil {
						return errInvalidInput("%v", err)
					}
				}
				return nil
			}, func(p browserctl.Permissions) error { return printPermissions(cmd, cfg, p) })
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "site <origin|host> <allow|block|clear>",
		Short:   "Allow or block a site for every kind of agent, or clear its rule",
		Example: "  monoagentcli browser permissions site https://github.com allow\n  monoagentcli browser permissions site bank.example block",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := opgate.Refusal(); why != "" {
				return operatorOnlyError("%s: an agent may not change browser permissions: ask the user to run `monoagentcli browser permissions site %s %s` or use Settings → Agent browser permissions", why, args[0], args[1])
			}
			return withPermissions(cfg, func(p *browserctl.Permissions) error {
				if _, _, err := p.SetSite(args[0], args[1]); err != nil {
					return errInvalidInput("%v", err)
				}
				return nil
			}, func(p browserctl.Permissions) error { return printPermissions(cmd, cfg, p) })
		},
	})
	return cmd
}

// withPermissions applies change (nil: none) to the permissions
// atomically (browserctl.UpdatePermissions), then hands them to done.
func withPermissions(cfg *globalConfig, change func(*browserctl.Permissions) error, done func(browserctl.Permissions) error) error {
	db, err := initDB(cfg)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer db.Close()
	var p browserctl.Permissions
	if change == nil {
		p, err = browserctl.LoadPermissions(db.DB)
	} else {
		p, err = browserctl.UpdatePermissions(db.DB, change)
	}
	if err != nil {
		return err
	}
	return done(p)
}

func printPermissions(cmd *cobra.Command, cfg *globalConfig, p browserctl.Permissions) error {
	doc := newPermissionsDoc(p)
	return browserOut(cmd, cfg, doc, func() {
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "AGENTS\tLEVEL\t")
		for _, k := range doc.Kinds {
			def := ""
			if k.IsDefault {
				def = "(default)"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", k.Label, k.Level, def)
		}
		_ = w.Flush()
		if len(doc.Sites) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "\nSITES")
			for _, s := range doc.Sites {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-6s %s\n", s.Rule, s.Origin)
			}
		}
		if doc.You.Declared != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "\nYou: %s (%s; you declared %s, which no token vouches for)\n", doc.You.Kind, doc.You.Level, doc.You.Declared)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "\nYou: %s (%s)\n", doc.You.Kind, doc.You.Level)
		}
	})
}
