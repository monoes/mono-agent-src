package librarycli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/cli/workflowio"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
)

type libPublishOptions struct {
	public, noBundle, asNew     bool
	name, description, tags, vv string
}

func newLibraryPublishCmd(e *libEnv) *cobra.Command {
	var o libPublishOptions
	cmd := &cobra.Command{
		Use:   "publish <workflow|automation|org|api> <workflow id | automation id | org name | API package id or file>",
		Short: "Upload a workflow, automation, org or API package to your monoes.me library (private unless --public)",
		Long: "Packs or exports the local item and uploads it:\n" +
			"  automation  the package as a .mpkg (as `automation export` writes it)\n" +
			"  workflow    the workflow JSON with the automations it uses bundled, except built-in and\n" +
			"              official monoes.me ones (the library installs those itself); --no-bundle for none\n" +
			"  org         the org document\n" +
			"  api         an API package (apipkg.v1): an installed one by id, or a package.json file or its folder,\n" +
			"              validated first\n\n" +
			"Publishing something you published (or installed from your own library item) before uploads a new " +
			"version of that item; --new always creates a new item. Items are private unless --public.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			kind, local := library.NormalizeKind(args[0]), args[1]
			if !library.ValidKind(kind) {
				return clicore.InvalidInput("kind must be workflow, automation, org or api, not %q", kind)
			}
			c, err := e.open()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			up, err := e.pack(ctx, kind, local, o)
			if err != nil {
				return err
			}
			if kind == library.KindAPI {
				local = strings.TrimSuffix(up.Filename, ".json") // a file publishes as its package id
			}
			me, err := c.Me(ctx)
			if err != nil {
				return e.deps.Err(err)
			}
			var it *library.Item
			created := true
			if prev := e.ownItemFor(ctx, c, kind, local, me.User.ID); prev != nil && !o.asNew {
				// An automation's version is its manifest's; the library
				// needs it to grow, so it is never made up here.
				if o.vv == "" && kind != library.KindAutomation {
					up.Version = bumpVersion(prev.Version)
				}
				it, err = c.PutArtifact(ctx, prev.ID, up)
				created = false
				if err == nil {
					it, err = e.syncListing(ctx, c, it, up, o)
				}
			} else {
				it, err = c.Create(ctx, up)
			}
			var ae *library.APIError
			if errors.As(err, &ae) && ae.Status == http.StatusConflict && kind == library.KindAutomation && !created {
				return clicore.InvalidInput("%v — raise the version in the package's automation.json (it must be newer than %s) and publish again", err, up.Version)
			}
			if err != nil {
				return e.deps.Err(err)
			}
			if err := e.recordPublished(it, kind, local); err != nil {
				return err
			}
			out := map[string]any{"item": it, "created": created}
			return e.print(cmd, out, func(w io.Writer) {
				verb := "Published"
				if !created {
					verb = "Published a new version of"
				}
				fmt.Fprintf(w, "%s %s %q %s (%s) on monoes.me\n", verb, it.Kind, it.Name, it.Version, it.Visibility)
				if it.URL != "" {
					fmt.Fprintln(w, it.URL)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&o.public, "public", false, "Anyone can list and install it (default: private, only you)")
	cmd.Flags().StringVar(&o.name, "name", "", "Name shown in the library (default: the local name)")
	cmd.Flags().StringVar(&o.description, "description", "", "Description (default: the local one)")
	cmd.Flags().StringVar(&o.tags, "tags", "", "Comma-separated tags")
	cmd.Flags().StringVar(&o.vv, "version", "", "Version (default: the package version, 1.0.0, or the next patch of your item)")
	cmd.Flags().BoolVar(&o.noBundle, "no-bundle", false, "Workflows: don't bundle the automations it uses")
	cmd.Flags().BoolVar(&o.asNew, "new", false, "Always create a new item, even if this was published before")
	return cmd
}

// syncListing applies the listing flags given on a republish (a new
// version keeps the item's name, description, tags and visibility
// otherwise). Visibility only ever widens here: --public makes a private
// item public; making it private again is done on monoes.me.
func (e *libEnv) syncListing(ctx context.Context, c *library.Client, it *library.Item, up library.Upload, o libPublishOptions) (*library.Item, error) {
	patch := map[string]any{}
	if o.public && it.Visibility == library.VisibilityPrivate {
		patch["visibility"] = library.VisibilityPublic
	}
	if o.name != "" && o.name != it.Name {
		patch["name"] = o.name
	}
	if o.description != "" && o.description != it.Description {
		patch["description"] = o.description
	}
	if o.tags != "" {
		patch["tags"] = up.Tags
	}
	if len(patch) == 0 {
		return it, nil
	}
	return c.Patch(ctx, it.ID, patch)
}

// pack builds the upload for a local workflow, automation, org or API
// package.
func (e *libEnv) pack(ctx context.Context, kind, local string, o libPublishOptions) (library.Upload, error) {
	up := library.Upload{Kind: kind, Visibility: library.VisibilityPrivate, Tags: splitCSV(o.tags), Version: o.vv}
	if o.public {
		up.Visibility = library.VisibilityPublic
	}
	var name, desc, version string
	switch kind {
	case library.KindAutomation:
		reg, err := e.registry()
		if err != nil {
			return up, err
		}
		info, err := reg.Info(local)
		if err != nil {
			if errors.Is(err, automation.ErrNotInstalled) {
				return up, clicore.NotFound("automation %q is not installed", local)
			}
			return up, err
		}
		var buf bytes.Buffer
		if err := reg.Export(local, &buf, automation.ExportOptions{}); err != nil {
			return up, clicore.InvalidInput("pack %s: %v", local, err)
		}
		up.Data, up.Filename, up.ContentType = buf.Bytes(), local+".mpkg", "application/zip"
		name, desc, version = info.Name, info.Description, info.Version
	case library.KindWorkflow:
		store := e.deps.OpenWorkflowStore(e.db)
		wf := workflowio.OwnedWorkflow(ctx, store, e.db.DB, e.deps.ProfileID(), local)
		if wf == nil {
			return up, clicore.NotFound("workflow %q not found in this profile", local)
		}
		var file any = workflowio.WorkflowFileFromWorkflow(wf)
		if !o.noBundle {
			b, err := e.deps.Bundle.BundleWorkflowAutomations(workflowio.WorkflowFileFromWorkflow(wf), workflowbundle.Options{SkipShipped: true})
			if err != nil {
				return up, err
			}
			file = b
		}
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			return up, err
		}
		up.Data, up.Filename, up.ContentType = data, slugFile(wf.Name)+".json", "application/json"
		name, desc, version = wf.Name, wf.Description, "1.0.0"
	case library.KindOrg:
		root := profiledir.Root(e.db.DB, e.deps.ProfileID())
		d, err := orgdesign.Load(root, local)
		if err != nil {
			return up, clicore.NotFound("org %q not found in this profile: %v", local, err)
		}
		data, err := json.MarshalIndent(d, "", "  ")
		if err != nil {
			return up, err
		}
		up.Data, up.Filename, up.ContentType = data, local+".json", "application/json"
		name, desc, version = d.Name, d.Goal, "1.0.0"
	case library.KindAPI:
		data, p, err := readAPIPackage(local)
		if err != nil {
			return up, err
		}
		up.Data, up.Filename, up.ContentType = data, p.ID+".json", "application/json"
		name, desc, version = p.Title, p.Description, "1.0.0"
	}
	up.Name, up.Description = nonEmptyStr(o.name, name), nonEmptyStr(o.description, desc)
	up.Version = nonEmptyStr(up.Version, version)
	return up, nil
}

func slugFile(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	if out := strings.Trim(b.String(), "-"); out != "" {
		return out
	}
	return "workflow"
}

// ownItemFor returns the library item this local thing was published as
// (or installed from), when it belongs to the logged-in user.
func (e *libEnv) ownItemFor(ctx context.Context, c *library.Client, kind, local, userID string) *library.Item {
	id := e.recordedItemID(kind, local)
	if id == "" {
		return nil
	}
	it, err := c.Get(ctx, id)
	if err != nil || it.Owner.ID == "" || it.Owner.ID != userID {
		return nil
	}
	return it
}

func (e *libEnv) recordedItemID(kind, local string) string {
	if kind == library.KindAutomation {
		if reg, err := e.registry(); err == nil {
			if info, err := reg.Info(local); err == nil && info.Library != nil {
				return info.Library.ItemID
			}
		}
	}
	prov, err := e.provenance()
	if err != nil {
		return ""
	}
	recs, _ := prov.List(e.deps.ProfileID(), kind)
	for _, r := range recs {
		if r.LocalID == local {
			return r.ItemID
		}
	}
	return ""
}

// recordPublished links the local thing to the item it was published as.
// An automation's own package keeps its source and trust; the link lives
// in the provenance file. Automations and API packages belong to no profile.
func (e *libEnv) recordPublished(it *library.Item, kind, local string) error {
	profile := e.deps.ProfileID()
	if kind == library.KindAutomation || kind == library.KindAPI {
		profile = ""
	}
	prov, err := e.provenance()
	if err != nil {
		return err
	}
	return prov.Put(library.Record{Kind: kind, Profile: profile, LocalID: local, ItemID: it.ID, Slug: it.Slug,
		Name: it.Name, Version: it.Version, SHA256: it.SHA256, Visibility: it.Visibility, Official: it.Official(),
		BaseURL: e.client.BaseURL, InstalledAt: time.Now().UTC()})
}

// bumpVersion is v with its patch number raised ("1.2.3" → "1.2.4");
// anything else gets ".1" appended.
func bumpVersion(v string) string {
	core, _, _ := strings.Cut(v, "+")
	core, _, _ = strings.Cut(core, "-")
	parts := strings.Split(core, ".")
	if len(parts) == 3 {
		if n, err := strconv.Atoi(parts[2]); err == nil {
			parts[2] = strconv.Itoa(n + 1)
			return strings.Join(parts, ".")
		}
	}
	if v == "" {
		return "1.0.0"
	}
	return v + ".1"
}
