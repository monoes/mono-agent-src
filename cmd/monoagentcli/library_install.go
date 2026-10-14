package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/library"
	"github.com/monoes/mono-agent/internal/workflow"
)

// libInstallOptions are `library install`'s flags.
type libInstallOptions struct {
	yes, dryRun, replace bool
	rename               string
}

// libInstallResult is `library install --json`.
type libInstallResult struct {
	Kind               string       `json:"kind"`
	Item               library.Item `json:"item"`
	SHA256             string       `json:"sha256"`
	DryRun             bool         `json:"dry_run"`
	Installed          bool         `json:"installed"`
	LocalID            string       `json:"local_id"`
	Trust              string       `json:"trust,omitempty"`
	Warnings           []string     `json:"warnings"`
	MissingAutomations []string     `json:"missing_automations"`
	Result             any          `json:"result"`
}

func newLibraryInstallCmd(e *libEnv) *cobra.Command {
	var o libInstallOptions
	cmd := &cobra.Command{
		Use:   "install <workflow|automation|org|api> <id | slug>  (or <kind>/<slug>)",
		Short: "Download a library item, verify its sha256 and install it into this profile (needs a login)",
		Long: "Downloads the item's artifact, checks it against the sha256 the library reports, and hands " +
			"it to the matching local install:\n" +
			"  automation  the automation package installer (official items get built-in trust; others install as imported)\n" +
			"  workflow    workflow import into the active profile, installing automations bundled in it\n" +
			"  org         the active profile's org folder; an org of the same name needs --rename <new> or --yes (replace)\n" +
			"  api         an API package, validated, then written to ~/.monoagent/apipkgs/<id>/package.json; its\n" +
			"              app.<id> node (or the native node it extends) is available to the next command. It may\n" +
			"              replace a built-in package; replacing an installed one from elsewhere needs --yes\n\n" +
			"Where the item came from is recorded, so `library update` can install newer versions.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 { // "automations/instagram", as a library page's URL names it
				k, slug, ok := strings.Cut(args[0], "/")
				if !ok {
					return errInvalidInput("usage: library install <workflow|automation|org|api> <id | slug>")
				}
				args = []string{k, k + "/" + slug}
			}
			kind, ref := library.NormalizeKind(args[0]), args[1]
			if !library.ValidKind(kind) {
				return errInvalidInput("kind must be workflow, automation, org or api, not %q", kind)
			}
			if o.rename != "" && kind != library.KindOrg {
				return errInvalidInput("--rename only applies to orgs")
			}
			ctx := cmd.Context()
			c, err := e.requireLogin(ctx)
			if err != nil {
				return err
			}
			it, err := resolveItem(ctx, c, kind, ref)
			if err != nil {
				return err
			}
			res, err := e.install(ctx, c, it, o)
			if err != nil {
				return err
			}
			return printLib(e.cfg, cmd, res, func(w io.Writer) { printInstall(w, res) })
		},
	}
	cmd.Flags().BoolVarP(&o.yes, "yes", "y", false, "Don't ask: replace an org or API package of the same name or an installed copy, install bundled automations")
	cmd.Flags().BoolVar(&o.dryRun, "dry-run", false, "Download and check, show what would be installed, change nothing")
	cmd.Flags().BoolVar(&o.replace, "replace", false, "Automations: replace an installed package that needs confirmation (your own or a more trusted one)")
	cmd.Flags().StringVar(&o.rename, "rename", "", "Orgs: install under this name instead")
	return cmd
}

// resolveItem finds kind's item by slug first, then by id.
func resolveItem(ctx context.Context, c *library.Client, kind, ref string) (*library.Item, error) {
	var it *library.Item
	var err error
	if k, slug, ok := strings.Cut(ref, "/"); ok {
		ref = library.NormalizeKind(k) + "/" + slug // "automations/instagram" as the web shows it
	}
	if !strings.Contains(ref, "/") {
		it, err = c.Get(ctx, kind+"/"+ref)
	}
	if it == nil {
		var ae *library.APIError
		if err != nil && !(errors.As(err, &ae) && ae.Status == 404) {
			return nil, libErr(err)
		}
		if it, err = c.Get(ctx, ref); err != nil {
			return nil, libErr(err)
		}
	}
	if it.Kind != kind {
		return nil, errInvalidInput("%s is a %s, not a %s", ref, it.Kind, kind)
	}
	return it, nil
}

// install downloads it (sha256-checked) and hands it to the kind's
// installer.
func (e *libEnv) install(ctx context.Context, c *library.Client, it *library.Item, o libInstallOptions) (*libInstallResult, error) {
	data, sum, err := c.Download(ctx, it)
	if err != nil {
		return nil, libErr(err)
	}
	res := &libInstallResult{Kind: it.Kind, Item: *it, SHA256: sum, DryRun: o.dryRun,
		Warnings: []string{}, MissingAutomations: []string{}}
	if res.Item.Tags == nil {
		res.Item.Tags = []string{}
	}
	switch it.Kind {
	case library.KindAutomation:
		err = e.installAutomation(it, data, sum, o, res)
	case library.KindWorkflow:
		err = e.installWorkflow(ctx, it, data, sum, o, res)
	case library.KindOrg:
		err = e.installOrg(ctx, it, data, sum, o, res)
	case library.KindAPI:
		err = e.installAPI(it, data, sum, o, res)
	}
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (e *libEnv) record(it *library.Item, kind, profile, localID, sum string) error {
	prov, err := e.provenance()
	if err != nil {
		return err
	}
	return prov.Put(library.Record{Kind: kind, Profile: profile, LocalID: localID, ItemID: it.ID, Slug: it.Slug,
		Name: it.Name, Version: it.Version, SHA256: sum, Visibility: it.Visibility, Official: it.Official(),
		BaseURL: e.client.BaseURL, InstalledAt: time.Now().UTC()})
}

// spool writes b to a private file under the registry home and returns it
// with its cleanup.
func (e *libEnv) spool(b []byte, pattern string) (string, func(), error) {
	reg, err := e.registry()
	if err != nil {
		return "", nil, err
	}
	dir := filepath.Join(reg.Home(), "library", ".downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", nil, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

func (e *libEnv) installAutomation(it *library.Item, data []byte, sum string, o libInstallOptions, res *libInstallResult) error {
	reg, err := e.registry()
	if err != nil {
		return err
	}
	path, cleanup, err := e.spool(data, "item-*.mpkg")
	if err != nil {
		return err
	}
	defer cleanup()
	origin := &automation.LibraryOrigin{ItemID: it.ID, Slug: it.Slug, Version: it.Version, SHA256: sum,
		Official: it.Official(), BaseURL: e.client.BaseURL}
	ir, err := reg.Install(path, automation.InstallOptions{DryRun: o.dryRun, ExpectSHA256: sum,
		Replace: o.replace || o.yes, Library: origin})
	if ir != nil {
		res.Result, res.LocalID, res.Trust = ir, ir.ID, ir.Review.Trust
		res.Warnings = append(res.Warnings, ir.Warnings...)
	}
	if err != nil {
		if errors.Is(err, automation.ErrReplaces) {
			return withInstallResult(errInvalidInput("%v — pass --replace (or --yes) to replace it", err), ir)
		}
		if errors.Is(err, automation.ErrInvalid) {
			return withInstallResult(errInvalidInput("%v", err), ir)
		}
		return withInstallResult(libErr(err), ir)
	}
	res.Installed = ir.Installed
	if ir.Review.Trust == automation.TrustBuiltin {
		res.Trust = automation.TrustBuiltin
	}
	return nil
}

// runSubcommand runs one of this binary's own commands in process, with
// --json output captured (the handoff to `workflow import`).
func (e *libEnv) runSubcommand(ctx context.Context, cmd *cobra.Command, args ...string) ([]byte, error) {
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.ExecuteContext(ctx)
	return out.Bytes(), err
}

func (e *libEnv) installWorkflow(ctx context.Context, it *library.Item, data []byte, sum string, o libInstallOptions, res *libInstallResult) error {
	wf, err := parseWorkflowDefinition(data)
	if err != nil {
		return errInvalidInput("the library's workflow file does not parse: %v", err)
	}
	if o.dryRun {
		res.LocalID = ""
		res.Result = map[string]any{"name": wf.Name, "nodes": len(wf.Nodes), "connections": len(wf.Connections)}
		res.MissingAutomations = e.missingAutomations(nodeTypes(wf.Nodes), it)
		return nil
	}
	path, cleanup, err := e.spool(data, "item-*.json")
	if err != nil {
		return err
	}
	defer cleanup()
	sub := *e.cfg
	sub.JSONOutput = true
	args := []string{"--file", path}
	if o.yes {
		args = append(args, "--yes")
	}
	// A reinstall of the same item replaces its earlier import in place.
	if prev := e.recordedLocalID(library.KindWorkflow, it.ID); prev != "" && e.localExists(ctx, library.KindWorkflow, prev) {
		args = append(args, "--replace", prev)
	}
	out, err := e.runSubcommand(ctx, newWorkflowImportCmd(&sub), args...)
	if err != nil {
		return err
	}
	var imp struct {
		ID                 string   `json:"id"`
		Status             string   `json:"status"`
		Warnings           []string `json:"warnings"`
		MissingAutomations []string `json:"missingAutomations"`
	}
	if err := json.Unmarshal(lastJSONObject(out), &imp); err != nil || imp.ID == "" {
		return fmt.Errorf("workflow import gave no result: %s", strings.TrimSpace(string(out)))
	}
	var full map[string]any
	_ = json.Unmarshal(lastJSONObject(out), &full)
	res.Result, res.LocalID, res.Installed = full, imp.ID, true
	res.Warnings = append(res.Warnings, imp.Warnings...)
	missing := e.missingAutomations(nodeTypes(wf.Nodes), it)
	res.MissingAutomations = mergeSorted(missing, imp.MissingAutomations)
	return e.record(it, library.KindWorkflow, e.cfg.ProfileID, imp.ID, sum)
}

// recordedLocalID is the local id an earlier install of item got here.
func (e *libEnv) recordedLocalID(kind, itemID string) string {
	prov, err := e.provenance()
	if err != nil {
		return ""
	}
	recs, _ := prov.List(e.cfg.ProfileID, kind)
	for _, r := range recs {
		if r.ItemID == itemID && r.Profile == e.cfg.ProfileID {
			return r.LocalID
		}
	}
	return ""
}

// lastJSONObject is b when it is one JSON value, else its last line that
// starts an object (output preceded by notices).
func lastJSONObject(b []byte) []byte {
	if t := bytes.TrimSpace(b); json.Valid(t) {
		return t
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if l := bytes.TrimSpace(lines[i]); len(l) > 0 && l[0] == '{' {
			return l
		}
	}
	return bytes.TrimSpace(b)
}

func nodeTypes(nodes []workflow.WorkflowNode) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Type)
	}
	return out
}

// missingAutomations names the automations a workflow needs that are not
// installed: node types this binary doesn't know whose prefix is an
// automation id, plus what the library's meta lists.
func (e *libEnv) missingAutomations(types []string, it *library.Item) []string {
	reg, err := e.registry()
	if err != nil {
		return []string{}
	}
	nodes := buildNodeRegistry(false, e.db.DB)
	builtin := map[string]bool{"trigger": true} // trigger.* run in the engine, not the registry
	for _, t := range nodes.Types() {
		if p, _, ok := strings.Cut(t, "."); ok {
			if _, err := reg.Info(p); err != nil {
				builtin[p] = true // a node namespace of the binary itself
			}
		}
	}
	want := map[string]bool{}
	for _, t := range types {
		if p, _, ok := strings.Cut(t, "."); ok && !nodes.Has(t) && !builtin[p] && automation.ValidID(p) {
			want[p] = true
		}
	}
	if raw, ok := it.Meta["required_automations"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && automation.ValidID(s) {
				want[s] = true
			}
		}
	}
	out := []string{}
	for id := range want {
		if info, err := reg.Info(id); err != nil || !info.Enabled {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func mergeSorted(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range append(append([]string{}, a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func printInstall(w io.Writer, r *libInstallResult) {
	verb := "Installed"
	if r.DryRun {
		verb = "Would install"
	}
	fmt.Fprintf(w, "%s %s %q %s from monoes.me", verb, r.Kind, r.Item.Name, r.Item.Version)
	if r.LocalID != "" {
		fmt.Fprintf(w, " as %s", r.LocalID)
	}
	fmt.Fprintf(w, " (sha256 %s verified)\n", shortHash(r.SHA256))
	if r.Trust != "" {
		fmt.Fprintf(w, "Trust: %s\n", r.Trust)
	}
	if a, ok := r.Result.(*apiInstallResult); ok && r.Installed {
		if a.Extends != "" {
			fmt.Fprintf(w, "Its operations are now part of the %s node (monoagentcli ref node %s).\n", a.Extends, a.Extends)
		} else {
			fmt.Fprintf(w, "Node type %s is now available in workflows (monoagentcli ref node %s).\n", a.NodeType, a.NodeType)
		}
	}
	for _, s := range r.Warnings {
		fmt.Fprintln(w, "Warning:", s)
	}
	if len(r.MissingAutomations) > 0 {
		fmt.Fprintf(w, "It needs automations that are not installed: %s\nInstall them with: monoagentcli library install automation <id>\n",
			strings.Join(r.MissingAutomations, ", "))
	}
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
