package automation

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/spf13/cobra"
)

// `automation new`: scaffold a package from data/automation-templates/<t>.

func (d Deps) newAutomationNewCmd() *cobra.Command {
	var tmpl, dir, name, startURL string
	var install bool
	cmd := &cobra.Command{
		Use:   "new <id>",
		Short: "Scaffold a new automation package from a template",
		Long: `Creates a package directory from a template: basic, login-form,
list-scrape, post-content or search-and-extract. The id is lower-case
letters, digits and dashes.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !automation.ValidID(id) {
				return fmt.Errorf("invalid id %q: use lower-case letters, digits and dashes", id)
			}
			if tmpl == "" {
				tmpl = "basic"
			}
			if dir == "" {
				dir = id
			}
			if name == "" {
				name = id
			}
			if startURL == "" {
				startURL = "https://example.com/"
				fmt.Fprintf(cmd.ErrOrStderr(), "note: no --start-url given; using %s — edit site.startUrl and site.domains in automation.json\n", startURL)
			}
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("%s already exists", dir)
			}
			if install {
				if err := d.refuseInstalledID(id); err != nil {
					return err
				}
			}
			tfs, err := automationTemplate(tmpl)
			if err != nil {
				return err
			}
			if err := scaffoldAutomation(tfs, dir, scaffoldVars{ID: id, Name: name, StartURL: startURL}); err != nil {
				os.RemoveAll(dir)
				return err
			}
			abs, _ := filepath.Abs(dir)
			out := cmd.OutOrStdout()
			if install {
				// The scaffold is the user's own package: local source and
				// trust, no review prompt. It stays on disk if this fails.
				res, err := d.installScaffold(abs)
				if err != nil {
					return fmt.Errorf("created %s, but installing it failed: %w", abs, err)
				}
				if d.JSONOutput() {
					return writeJSONTo(out, map[string]any{"dir": abs, "install": res})
				}
				fmt.Fprintf(out, "Created %s from template %q and installed %s %s (local)\n", abs, tmpl, res.ID, res.Version)
				return nil
			}
			if d.JSONOutput() {
				return writeJSONTo(out, map[string]string{"dir": abs})
			}
			fmt.Fprintf(out, "Created %s from template %q\n", abs, tmpl)
			fmt.Fprintf(out, "Next: edit it, then 'monoagentcli automation validate %s' and 'automation install %s --local'\n", dir, dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&tmpl, "template", "basic", "Template name")
	cmd.Flags().StringVar(&dir, "dir", "", "Target directory (default ./<id>)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (default: the id)")
	cmd.Flags().BoolVar(&install, "install", false, "Also install the new package (as local)")
	cmd.Flags().StringVar(&startURL, "start-url", "", "Site start URL; its host becomes the allowed domain")
	return cmd
}

// refuseInstalledID stops `new --install` from overwriting an automation
// that is already installed under id (a removed built-in does not count).
// Checked before anything is written.
func (d Deps) refuseInstalledID(id string) error {
	reg, err := d.OpenRegistry()
	if err != nil {
		return err
	}
	if info, err := reg.Info(id); err == nil && info != nil && !info.Removed {
		return fmt.Errorf("automation %s already exists; pick another id or use `automation install <dir> --local --replace`", id)
	}
	return nil
}

// installScaffold installs a freshly scaffolded package directory as local.
func (d Deps) installScaffold(dir string) (*automation.InstallResult, error) {
	reg, err := d.OpenRegistry()
	if err != nil {
		return nil, err
	}
	res, err := reg.Install(dir, automation.InstallOptions{Source: automation.SourceLocal, Trust: automation.TrustLocal})
	return res, d.WithInstallResult(err, res)
}

// automationTemplate returns the named template tree.
func automationTemplate(name string) (fs.FS, error) {
	root, err := automationTemplatesRoot()
	if err != nil {
		return nil, err
	}
	if !safeName(name) {
		return nil, fmt.Errorf("invalid template name %q", name)
	}
	if _, err := fs.Stat(root, name+"/automation.json"); err != nil {
		return nil, fmt.Errorf("unknown template %q (available: %s)", name, strings.Join(automationTemplateNames(root), ", "))
	}
	return fs.Sub(root, name)
}

func automationTemplateNames(root fs.FS) []string {
	var out []string
	entries, _ := fs.ReadDir(root, ".")
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

type scaffoldVars struct {
	ID, Name, StartURL string
}

// scaffoldAutomation copies tfs into dir, filling the {{id}}, {{name}},
// {{startUrl}} and {{domain}} placeholders (data/automation-templates/
// README.md). In .json files values are inserted JSON-string-escaped; any
// other {{…}} is a run-time variable and left alone.
func scaffoldAutomation(tfs fs.FS, dir string, v scaffoldVars) error {
	u, err := url.Parse(v.StartURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("invalid --start-url %q", v.StartURL)
	}
	startURL := v.StartURL
	if !strings.HasSuffix(startURL, "/") {
		startURL += "/" // templates append paths such as "login"
	}
	vals := []string{"{{id}}", v.ID, "{{name}}", v.Name, "{{startUrl}}", startURL, "{{domain}}", u.Hostname()}
	plain := strings.NewReplacer(vals...)
	escaped := make([]string, len(vals))
	for i, s := range vals {
		if i%2 == 1 {
			b, _ := json.Marshal(s)
			s = string(b[1 : len(b)-1])
		}
		escaped[i] = s
	}
	inJSON := strings.NewReplacer(escaped...)

	return fs.WalkDir(tfs, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, err := fs.ReadFile(tfs, p)
		if err != nil {
			return err
		}
		switch {
		case strings.EqualFold(filepath.Ext(p), ".json"):
			b = []byte(inJSON.Replace(string(b)))
		case isTextFile(p):
			b = []byte(plain.Replace(string(b)))
		}
		return os.WriteFile(dst, b, 0o644)
	})
}

func isTextFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".json", ".md", ".js", ".html", ".txt", ".svg":
		return true
	}
	return false
}
