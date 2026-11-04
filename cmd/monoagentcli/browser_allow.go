package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// browser allow list · add · remove — the origins an agent may drive
// besides local and dev pages. Only the user changes them.

func newBrowserAllowCmd(cfg *globalConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allow",
		Short: "The sites agents may drive besides local and dev pages",
		Long: "Agents drive local and dev pages by default. Add an origin (https://example.com,\n" +
			"http://staging.example.com:8080) or a wildcard (*.example.com: the domain and every\n" +
			"subdomain) to let them drive it too; a form submit there still waits for your\n" +
			"approval. Only you can change the list: an agent is refused.",
	}
	cmd.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "List the allowed origins",
		Example: "  monoagentcli --json browser allow list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withBrowserAllowlist(cfg, nil, func(list []string) error {
				return browserOut(cmd, cfg, map[string]any{"origins": list}, func() {
					if len(list) == 0 {
						fmt.Fprintln(cmd.OutOrStdout(), "No origins allowed besides local and dev pages.")
					}
					for _, o := range list {
						fmt.Fprintln(cmd.OutOrStdout(), o)
					}
				})
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "add <origin>",
		Short:   "Let agents drive an origin (scheme://host[:port] or *.host)",
		Example: "  monoagentcli browser allow add https://staging.example.com\n  monoagentcli browser allow add '*.example.com'",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := opgate.Refusal(); why != "" {
				return operatorOnlyError("%s: an agent may not change which sites agents drive: ask the user to run `monoagentcli browser allow add %s` in their own terminal", why, args[0])
			}
			entry, err := browserctl.NormalizeAllowEntry(args[0])
			if err != nil {
				return errInvalidInput("%v", err)
			}
			return withBrowserAllowlist(cfg, func(p *browserctl.Permissions) error {
				_, _, err := p.SetSite(entry, browserctl.RuleAllow)
				return err
			}, func(list []string) error {
				return browserOut(cmd, cfg, map[string]any{"origins": list, "added": entry}, func() {
					fmt.Fprintf(cmd.OutOrStdout(), "Agents may now drive %s\n", entry)
				})
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:     "remove <origin>",
		Short:   "Stop agents driving an origin",
		Example: "  monoagentcli browser allow remove https://staging.example.com",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if why := opgate.Refusal(); why != "" {
				return operatorOnlyError("%s: an agent may not change which sites agents drive: ask the user to run `monoagentcli browser allow remove %s` in their own terminal", why, args[0])
			}
			entry, err := browserctl.NormalizeAllowEntry(args[0])
			if err != nil {
				return errInvalidInput("%v", err)
			}
			return withBrowserAllowlist(cfg, func(p *browserctl.Permissions) error {
				if p.Rule(entry) != browserctl.RuleAllow {
					return errNotFound("%s is not on the list", entry)
				}
				_, _, err := p.SetSite(entry, "clear")
				return err
			}, func(list []string) error {
				return browserOut(cmd, cfg, map[string]any{"origins": list, "removed": entry}, func() {
					fmt.Fprintf(cmd.OutOrStdout(), "Agents no longer drive %s\n", entry)
				})
			})
		},
	})
	return cmd
}

// withBrowserAllowlist applies change (nil: none) to the permissions
// atomically (browserctl.UpdatePermissions), and hands the allow rules
// after it to done.
func withBrowserAllowlist(cfg *globalConfig, change func(*browserctl.Permissions) error, done func([]string) error) error {
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
	return done(p.Sited(browserctl.RuleAllow))
}
