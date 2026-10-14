package action

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	actiondef "github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/autoinstall"
	"github.com/spf13/cobra"
)

// `action export` / `action import`: single actions moved between machines
// as minimal packages (spec §5.3).

func (d Deps) newActionExportCmd() *cobra.Command {
	var outFile string
	var df autoinstall.DomainFlags
	cmd := &cobra.Command{
		Use:   "export <automation>.<action>",
		Short: "Export one action, with the fragments/selectors/scripts it uses, as a .mpkg",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, name, ok := strings.Cut(args[0], ".")
			if !ok || id == "" || name == "" {
				return fmt.Errorf("expected <automation>.<action>, got %q", args[0])
			}
			reg, err := d.Install.OpenRegistry()
			if err != nil {
				return err
			}
			if outFile == "" {
				outFile = fmt.Sprintf("%s.%s.mpkg", id, name)
			}
			doms, err := df.Resolve(reg, id)
			if err != nil {
				return err
			}
			opts := automation.ExportOptions{Actions: []string{name}, Domains: doms}
			sum, err := autoinstall.WriteHashed(outFile, func(w io.Writer) error { return reg.Export(id, w, opts) })
			if err != nil {
				return err
			}
			return d.Install.PrintFileResult(cmd.OutOrStdout(), outFile, sum, "Exported")
		},
	}
	cmd.Flags().StringVarP(&outFile, "output", "o", "", "Output file (default <automation>.<action>.mpkg)")
	df.Register(cmd)
	return cmd
}

func (d Deps) newActionImportCmd() *cobra.Command {
	var into string
	var yes, dryRun, replace, replaceBuiltin bool
	cmd := &cobra.Command{
		Use:   "import <file.mpkg|dir|action.json>",
		Short: "Merge the actions of a package (or one action file) into an installed automation",
		Long: `Merges every action of the source into --into (default: the source's own
automation id). When that automation is not installed it is created.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.Install.OpenRegistry()
			if err != nil {
				return err
			}
			src, cleanup, err := openActionSource(args[0], reg)
			if err != nil {
				return err
			}
			defer cleanup()
			target := into
			if target == "" {
				target = src.Manifest.ID
			}
			c := autoinstall.Confirmer{Yes: yes, Interactive: !d.Install.JSONOutput() && d.Install.StdinIsTerminal(),
				In: cmd.InOrStdin(), Out: cmd.ErrOrStderr()}
			res, err := d.Install.RunInstall(automation.InstallOptions{DryRun: dryRun, Source: actionImportSource(args[0]), Replace: replace || replaceBuiltin}, c, func(o automation.InstallOptions) (*automation.InstallResult, error) {
				return addActions(reg, target, src, o)
			})
			if err != nil {
				d.Install.PrintFailedIssues(cmd, err)
				return err
			}
			return d.Install.PrintInstallResult(cmd.OutOrStdout(), res)
		},
	}
	cmd.Flags().StringVar(&into, "into", "", "Target automation id (default: the source's id)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Import without asking")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the review without importing")
	cmd.Flags().BoolVar(&replace, "replace", false, "Confirm merging into an automation that needs it (built-in, local or recorded)")
	cmd.Flags().BoolVar(&replaceBuiltin, "replace-builtin", false, "Deprecated alias of --replace")
	_ = cmd.Flags().MarkHidden("replace-builtin")
	return cmd
}

// actionImportSource is local for a loose action file (the user's own work) and
// imported for a package that came from elsewhere.
func actionImportSource(src string) string {
	if strings.EqualFold(filepath.Ext(src), ".json") {
		return automation.SourceLocal
	}
	return automation.SourceImported
}

// addActions merges every action of src into target. The result is the last
// AddAction's, with warnings and issues of all of them.
func addActions(reg *automation.Registry, target string, src *automation.Package, o automation.InstallOptions) (*automation.InstallResult, error) {
	if len(src.Manifest.Actions) == 0 {
		return nil, fmt.Errorf("%s has no actions", src.Manifest.ID)
	}
	var last *automation.InstallResult
	var warnings []string
	var issues []automation.IssueJSON
	for _, name := range src.Manifest.Actions {
		res, err := reg.AddAction(target, src, name, o)
		if err != nil {
			return res, fmt.Errorf("add %s.%s: %w", target, name, err)
		}
		warnings = append(warnings, res.Warnings...)
		issues = append(issues, res.Issues...)
		last = res
	}
	last.Warnings, last.Issues = warnings, issues
	return last, nil
}

// openActionSource opens a .mpkg, a package directory, or a loose action
// JSON (wrapped into a generated package). cleanup removes any temp dir.
func openActionSource(src string, reg *automation.Registry) (*automation.Package, func(), error) {
	noop := func() {}
	st, err := os.Stat(src)
	if err != nil {
		return nil, noop, err
	}
	switch {
	case st.IsDir():
		p, err := automation.OpenDir(src)
		return p, noop, err
	case strings.EqualFold(filepath.Ext(src), ".json"):
		return wrapActionJSON(src, reg)
	default:
		p, err := automation.OpenFile(src)
		return p, noop, err
	}
}

// socialAutomationIDs are legacy platform ids whose wrapped packages must
// carry the social policy tier, since a loose action declares no domains.
var socialAutomationIDs = map[string]bool{
	"instagram": true, "linkedin": true, "x": true, "twitter": true,
	"tiktok": true, "facebook": true, "threads": true,
}

// wrapActionJSON turns one legacy ActionDef file into a generated local
// package in a temp dir: id = its automation (or platform) field. The site
// (and policy/native bot) come from the installed automation with that id
// when there is one, else from the action's absolute navigate URLs.
func wrapActionJSON(file string, reg *automation.Registry) (*automation.Package, func(), error) {
	noop := func() {}
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, noop, err
	}
	var def actiondef.ActionDef
	if err := json.Unmarshal(raw, &def); err != nil {
		return nil, noop, fmt.Errorf("invalid ActionDef JSON: %w", err)
	}
	id := strings.ToLower(strings.TrimSpace(def.Automation))
	if id == "" {
		id = strings.ToLower(strings.TrimSpace(def.Platform))
	}
	name := strings.ToLower(strings.TrimSpace(def.ActionType))
	switch {
	case id == "":
		return nil, noop, fmt.Errorf("ActionDef missing required field: automation (or platform)")
	case name == "":
		return nil, noop, fmt.Errorf("ActionDef missing required field: actionType")
	case len(def.Steps) == 0:
		return nil, noop, fmt.Errorf("ActionDef has no steps")
	case !safeName(id) || !safeName(name):
		return nil, noop, fmt.Errorf("automation or actionType contains invalid characters")
	}
	dir, err := os.MkdirTemp("", "monoagent-action-*")
	if err != nil {
		return nil, noop, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	tier := "standard"
	if socialAutomationIDs[id] {
		tier = "social"
	}
	m := automation.Manifest{
		Schema:      automation.SchemaV1,
		ID:          id,
		Name:        id,
		Version:     "0.1.0",
		Description: "Generated from " + filepath.Base(file),
		Site:        automation.Site{Domains: navigateHosts(def.Steps)},
		Permissions: automation.Permissions{Steps: stepTypes(def.Steps), Scripts: []string{}},
		Actions:     []string{name},
		Policy:      automation.Policy{Tier: tier},
	}
	if reg != nil {
		if base, err := reg.Get(id); err == nil {
			bm := base.Manifest
			m.Name, m.Site, m.Policy, m.Requires = bm.Name, bm.Site, bm.Policy, bm.Requires
		}
	}
	if len(m.Site.Domains) == 0 && m.Requires.Native == "" {
		cleanup()
		return nil, noop, fmt.Errorf("cannot tell which site %s.%s runs on: add a navigate step with an absolute URL, or scaffold a package with 'automation new'", id, name)
	}
	if m.Site.StartURL == "" && len(m.Site.Domains) > 0 {
		m.Site.StartURL = "https://" + m.Site.Domains[0] + "/"
	}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err = os.MkdirAll(filepath.Join(dir, "actions"), 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, "automation.json"), mb, 0o644)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "actions", name+".json"), raw, 0o644)
		}
	}
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	p, err := automation.OpenDir(dir)
	if err != nil {
		cleanup()
		return nil, noop, err
	}
	return p, cleanup, nil
}

// stepTypes returns the sorted step types used in steps (nested included):
// a wrapped action is permitted exactly what it already does.
func stepTypes(steps []actiondef.StepDef) []string {
	seen := map[string]bool{}
	var walk func([]actiondef.StepDef)
	walk = func(steps []actiondef.StepDef) {
		for _, s := range steps {
			seen[s.Type] = true
			walk(s.Steps)
		}
	}
	walk(steps)
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// navigateHosts returns the hosts of absolute http(s) navigate URLs in steps
// (nested included), in first-seen order. Templated URLs are skipped.
func navigateHosts(steps []actiondef.StepDef) []string {
	out := []string{}
	seen := map[string]bool{}
	var walk func([]actiondef.StepDef)
	walk = func(steps []actiondef.StepDef) {
		for _, s := range steps {
			walk(s.Steps)
			if s.Type != "navigate" || strings.Contains(s.URL, "{{") {
				continue
			}
			u, err := url.Parse(s.URL)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
				continue
			}
			if h := strings.ToLower(u.Hostname()); !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	walk(steps)
	return out
}

// safeName reports whether s is usable as a path element: no separators,
// no "..", no leading dot.
func safeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..") && !strings.HasPrefix(s, ".")
}
