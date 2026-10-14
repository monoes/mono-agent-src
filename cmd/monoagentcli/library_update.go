package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/library"
)

// libUpdate is one row of `library update`.
type libUpdate struct {
	Kind    string `json:"kind"`
	LocalID string `json:"local_id"`
	ItemID  string `json:"item_id"`
	Name    string `json:"name"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Status: updated | available (dry run) | up_to_date | needs_confirmation | gone | failed
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`

	loginRequired bool
}

func newLibraryUpdateCmd(e *libEnv) *cobra.Command {
	var yes, dryRun bool
	cmd := &cobra.Command{
		Use:   "update [local id | item id | slug]",
		Short: "Install newer library versions of what came from monoes.me",
		Long: "Checks every workflow, org, automation and API package installed from the library (or one of them) " +
			"and installs newer versions. Built-in automations an older MonoAgent installed are matched " +
			"to their official monoes.me items first. Replacing an org needs --yes. Needs a login " +
			"(`monoagentcli library login`).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := e.requireLogin(ctx)
			if err != nil {
				return err
			}
			e.adoptOfficial(ctx, c)
			recs, err := e.installedRecords(ctx, "")
			if err != nil {
				return err
			}
			var only string
			if len(args) == 1 {
				only = args[0]
			}
			updates := []libUpdate{}
			for _, r := range recs {
				if only != "" && only != r.LocalID && only != r.ItemID && only != r.Slug {
					continue
				}
				u := e.updateOne(ctx, c, r, yes, dryRun)
				if u.loginRequired {
					return libErr(library.ErrNotLoggedIn) // the login expired: every other item would fail the same way
				}
				updates = append(updates, u)
			}
			if only != "" && len(updates) == 0 {
				return errNotFound("nothing installed from monoes.me matches %q (see `monoagentcli library installed`)", only)
			}
			out := map[string]any{"updates": updates}
			if err := printLib(e.cfg, cmd, out, func(w io.Writer) { printUpdates(w, updates) }); err != nil {
				return err
			}
			for _, u := range updates {
				if u.Status == "failed" {
					return reportedError{fmt.Errorf("some updates failed")}
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Replace orgs and packages that need confirmation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Only report what has a newer version")
	return cmd
}

func (e *libEnv) updateOne(ctx context.Context, c *library.Client, r library.Record, yes, dryRun bool) libUpdate {
	u := libUpdate{Kind: r.Kind, LocalID: r.LocalID, ItemID: r.ItemID, Name: r.Name, From: r.Version, To: r.Version}
	it, err := c.Get(ctx, r.ItemID)
	if err != nil {
		var ae *library.APIError
		if isLoginRequired(err) {
			u.loginRequired = true
		} else if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			u.Status, u.Error = "gone", "the item is no longer in the library (or no longer visible to you)"
		} else {
			u.Status, u.Error = "failed", err.Error()
		}
		return u
	}
	u.Name, u.To = it.Name, it.Version
	if !newerThan(it, r.Version, r.SHA256) {
		u.Status = "up_to_date"
		return u
	}
	if dryRun {
		u.Status = "available"
		return u
	}
	opts := libInstallOptions{yes: yes}
	if r.Kind == library.KindOrg {
		opts.rename = r.LocalID // update the org it was installed as
	}
	if _, err := e.install(ctx, c, it, opts); err != nil {
		u.Status, u.Error, u.loginRequired = "failed", err.Error(), isLoginRequired(err)
		var ce *cliError
		if errors.As(err, &ce) && ce.code == 3 && !yes {
			u.Status = "needs_confirmation"
		}
		return u
	}
	u.Status = "updated"
	return u
}

// adoptOfficial matches built-ins an older release seeded to their
// official library items (by package id), so `library update` covers
// them. Best effort: offline, nothing is adopted.
func (e *libEnv) adoptOfficial(ctx context.Context, c *library.Client) {
	li, err := e.localIndex(ctx)
	if err != nil {
		return
	}
	need := false
	for _, p := range li.pkgs {
		if p.Source == automation.SourceBuiltin && p.Library == nil {
			need = true
		}
	}
	if !need {
		return
	}
	res, err := c.List(ctx, library.ListQuery{Kind: library.KindAutomation, Scope: "official", PerPage: 100})
	if err != nil {
		return
	}
	for _, it := range res.Items {
		li.annotate(it)
	}
}

func printUpdates(w io.Writer, updates []libUpdate) {
	if len(updates) == 0 {
		fmt.Fprintln(w, "Nothing installed from monoes.me yet.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tLOCAL ID\tFROM\tTO\tSTATUS")
	for _, u := range updates {
		st := u.Status
		if u.Error != "" {
			st += ": " + u.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", u.Kind, u.LocalID, u.From, u.To, st)
	}
	tw.Flush()
}
