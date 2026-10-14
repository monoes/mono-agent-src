package librarycli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/workflowio"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
)

// Installed is an item's local copy, as list/show report it.
type Installed struct {
	LocalID         string `json:"local_id"`
	Version         string `json:"version"`
	SHA256          string `json:"sha256,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
}

// Item is a library item plus what this machine has of it.
type Item struct {
	library.Item
	Installed *Installed `json:"installed"`
}

// newerThan reports whether the library item is newer than the local copy:
// a higher version, or the same version with different bytes.
func newerThan(it *library.Item, version, sha string) bool {
	if c := automation.CompareVersions(it.Version, version); c != 0 {
		return c > 0
	}
	return sha != "" && it.SHA256 != "" && !strings.EqualFold(sha, it.SHA256)
}

// localIndex answers "is this item installed here?" for the active profile.
type localIndex struct {
	byItem map[string]library.Record // item id → record (workflows, orgs, automations)
	pkgs   map[string]automation.InstalledInfo
	reg    *automation.Registry
}

func (e *libEnv) localIndex(ctx context.Context) (*localIndex, error) {
	li := &localIndex{byItem: map[string]library.Record{}, pkgs: map[string]automation.InstalledInfo{}}
	reg, err := e.registry()
	if err != nil {
		return nil, err
	}
	li.reg = reg
	pkgs, err := reg.List(false)
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		li.pkgs[p.ID] = p
		if o := p.Library; o != nil && o.ItemID != "" {
			li.byItem[o.ItemID] = library.Record{Kind: library.KindAutomation, LocalID: p.ID, ItemID: o.ItemID,
				Version: nonEmptyStr(o.Version, p.Version), SHA256: o.SHA256, Official: o.Official, Slug: o.Slug}
		}
	}
	prov, err := e.provenance()
	if err != nil {
		return nil, err
	}
	recs, err := prov.List(e.deps.ProfileID(), "")
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if _, seen := li.byItem[r.ItemID]; seen {
			continue
		}
		if e.recordLive(ctx, li, r) {
			li.byItem[r.ItemID] = r
		}
	}
	return li, nil
}

// recordLive reports whether a provenance record's local copy is still
// there. Automation records are the user's own packages they published.
func (e *libEnv) recordLive(ctx context.Context, li *localIndex, r library.Record) bool {
	if r.Kind == library.KindAutomation {
		_, ok := li.pkgs[r.LocalID]
		return ok
	}
	return e.localExists(ctx, r.Kind, r.LocalID)
}

// localExists reports whether a workflow, org or API package the library
// installed is still there (a deleted one is simply not installed any more).
func (e *libEnv) localExists(ctx context.Context, kind, id string) bool {
	switch kind {
	case library.KindWorkflow:
		return workflowio.OwnedWorkflow(ctx, e.deps.OpenWorkflowStore(e.db), e.db.DB, e.deps.ProfileID(), id) != nil
	case library.KindOrg:
		path, err := orgdesign.ConfigPath(profiledir.Root(e.db.DB, e.deps.ProfileID()), id)
		if err != nil {
			return false
		}
		_, err = os.Stat(path)
		return err == nil
	case library.KindAPI:
		return apiPackageInstalled(id)
	}
	return false
}

// annotate attaches the local copy to it. An official automation whose id
// matches a built-in an older release seeded adopts it: the built-in is
// recorded as coming from this item, so `library update` can update it.
func (li *localIndex) annotate(it library.Item) Item {
	out := Item{Item: it}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if r, ok := li.byItem[it.ID]; ok {
		out.Installed = &Installed{LocalID: r.LocalID, Version: r.Version, SHA256: r.SHA256,
			UpdateAvailable: newerThan(&it, r.Version, r.SHA256)}
		return out
	}
	if it.Kind != library.KindAutomation || !it.Official() {
		return out
	}
	p, ok := li.pkgs[it.AutomationID()]
	if !ok || p.Source != automation.SourceBuiltin || (p.Library != nil && p.Library.ItemID != "") {
		return out
	}
	origin := &automation.LibraryOrigin{ItemID: it.ID, Slug: it.Slug, Version: p.Version, Official: true, BaseURL: ""}
	if adopted, err := li.reg.SetLibraryOrigin(p.ID, origin); err == nil && adopted {
		li.byItem[it.ID] = library.Record{Kind: library.KindAutomation, LocalID: p.ID, ItemID: it.ID, Version: p.Version, Official: true}
	}
	out.Installed = &Installed{LocalID: p.ID, Version: p.Version, UpdateAvailable: newerThan(&it, p.Version, "")}
	return out
}

func newLibraryListCmd(e *libEnv) *cobra.Command {
	var q library.ListQuery
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List library items (official and public by default; --scope mine for your own; needs a login)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			q.Kind = library.NormalizeKind(q.Kind)
			if q.Kind != "" && !library.ValidKind(q.Kind) {
				return clicore.InvalidInput("--kind must be workflow, automation, org or api")
			}
			switch q.Scope {
			case "", "public", "official", "mine":
			default:
				return clicore.InvalidInput("--scope must be public, official or mine")
			}
			if q.Scope == "" {
				q.Scope = "public"
			}
			if q.PerPage > 100 {
				return clicore.InvalidInput("--per-page is at most 100")
			}
			ctx := cmd.Context()
			c, err := e.requireLogin(ctx)
			if err != nil {
				return err
			}
			res, err := c.List(ctx, q)
			if err != nil {
				return e.deps.Err(err)
			}
			li, err := e.localIndex(ctx)
			if err != nil {
				return err
			}
			items := make([]Item, 0, len(res.Items))
			for _, it := range res.Items {
				items = append(items, li.annotate(it))
			}
			out := map[string]any{"items": items, "page": res.Page, "per_page": res.PerPage, "total": res.Total, "scope": q.Scope}
			return e.print(cmd, out, func(w io.Writer) { printItemTable(w, items, res.Total) })
		},
	}
	cmd.Flags().StringVar(&q.Kind, "kind", "", "workflow, automation, org or api (default: all)")
	cmd.Flags().StringVar(&q.Scope, "scope", "public", "public (public + official), official, or mine")
	cmd.Flags().StringVar(&q.Search, "search", "", "Search names and descriptions")
	cmd.Flags().StringVar(&q.Tag, "tag", "", "Only items with this tag")
	cmd.Flags().IntVar(&q.Page, "page", 1, "Page number")
	cmd.Flags().IntVar(&q.PerPage, "per-page", 20, "Items per page (at most 100)")
	return cmd
}

func printItemTable(w io.Writer, items []Item, total int) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No items.")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tSLUG\tVERSION\tVISIBILITY\tOWNER\tINSTALLED\tNAME")
	for _, it := range items {
		inst := "-"
		if it.Installed != nil {
			inst = it.Installed.Version
			if it.Installed.UpdateAvailable {
				inst += " (update)"
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", it.Kind, it.Slug, it.Version, it.Visibility, it.Owner.Username, inst, it.Name)
	}
	tw.Flush()
	if total > len(items) {
		fmt.Fprintf(w, "\n%d of %d shown (--page for more).\n", len(items), total)
	}
	fmt.Fprintln(w, "\nInstall one: monoagentcli library install <kind> <slug-or-id>")
}

func newLibraryShowCmd(e *libEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id | kind/slug>",
		Short: "Show one library item (needs a login)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := e.requireLogin(ctx)
			if err != nil {
				return err
			}
			ref := args[0]
			if k, slug, ok := strings.Cut(ref, "/"); ok {
				ref = library.NormalizeKind(k) + "/" + slug
			}
			it, err := c.Get(ctx, ref)
			if err != nil {
				return e.deps.Err(err)
			}
			li, err := e.localIndex(ctx)
			if err != nil {
				return err
			}
			out := li.annotate(*it)
			return e.print(cmd, out, func(w io.Writer) {
				fmt.Fprintf(w, "%s (%s %s, %s)\n", out.Name, out.Kind, out.Version, out.Visibility)
				fmt.Fprintf(w, "  id:      %s\n  slug:    %s\n  owner:   %s\n", out.ID, out.Slug, out.Owner.Username)
				if out.Description != "" {
					fmt.Fprintf(w, "  about:   %s\n", out.Description)
				}
				if len(out.Tags) > 0 {
					fmt.Fprintf(w, "  tags:    %s\n", strings.Join(out.Tags, ", "))
				}
				fmt.Fprintf(w, "  sha256:  %s (%d bytes)\n", out.SHA256, out.Size)
				if out.URL != "" {
					fmt.Fprintf(w, "  page:    %s\n", out.URL)
				}
				if out.Installed != nil {
					fmt.Fprintf(w, "  installed as %s %s", out.Installed.LocalID, out.Installed.Version)
					if out.Installed.UpdateAvailable {
						fmt.Fprint(w, " — update available: monoagentcli library update ", out.Installed.LocalID)
					}
					fmt.Fprintln(w)
				} else {
					fmt.Fprintf(w, "\nInstall: monoagentcli library install %s %s\n", out.Kind, out.Slug)
				}
			})
		},
	}
}

// installedRecords is every library record for the profile: workflows and
// orgs from the provenance file, automations from the registry index.
func (e *libEnv) installedRecords(ctx context.Context, kind string) ([]library.Record, error) {
	if _, err := e.open(); err != nil {
		return nil, err
	}
	li, err := e.localIndex(ctx)
	if err != nil {
		return nil, err
	}
	prov, _ := e.provenance()
	recs, err := prov.List(e.deps.ProfileID(), kind)
	if err != nil {
		return nil, err
	}
	out := []library.Record{}
	fromIndex := map[string]bool{}
	if kind == "" || kind == library.KindAutomation {
		for _, id := range sortedKeys(li.pkgs) {
			p := li.pkgs[id]
			if o := p.Library; o != nil && o.ItemID != "" {
				vis := ""
				if o.Official {
					vis = library.VisibilityOfficial
				}
				fromIndex[p.ID] = true
				out = append(out, library.Record{Kind: library.KindAutomation, LocalID: p.ID, Source: "monoes",
					ItemID: o.ItemID, Slug: o.Slug, Name: p.Name, Version: nonEmptyStr(o.Version, p.Version),
					SHA256: o.SHA256, Official: o.Official, BaseURL: o.BaseURL, InstalledAt: p.InstalledAt, Visibility: vis})
			}
		}
	}
	for _, r := range recs {
		if r.Kind == library.KindAutomation && fromIndex[r.LocalID] {
			continue
		}
		if e.recordLive(ctx, li, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func newLibraryInstalledCmd(e *libEnv) *cobra.Command {
	var kind string
	cmd := &cobra.Command{
		Use:   "installed",
		Short: "List what this profile installed from the library",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			kind = library.NormalizeKind(kind)
			if kind != "" && !library.ValidKind(kind) {
				return clicore.InvalidInput("--kind must be workflow, automation, org or api")
			}
			recs, err := e.installedRecords(cmd.Context(), kind)
			if err != nil {
				return err
			}
			return e.print(cmd, map[string]any{"items": recs}, func(w io.Writer) {
				if len(recs) == 0 {
					fmt.Fprintln(w, "Nothing installed from monoes.me yet.")
					return
				}
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "KIND\tLOCAL ID\tVERSION\tITEM\tOFFICIAL")
				for _, r := range recs {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\n", r.Kind, r.LocalID, r.Version, nonEmptyStr(r.Slug, r.ItemID), r.Official)
				}
				tw.Flush()
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "workflow, automation, org or api (default: all)")
	return cmd
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
