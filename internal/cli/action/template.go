package action

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	actiondef "github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/statedir"
	"github.com/spf13/cobra"
)

// userCaptureDir returns ~/.monoagent/captures, creating it if needed.
func userCaptureDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".monoagent", "captures")
	if err := statedir.MkdirAll(dir); err != nil {
		return "", fmt.Errorf("create captures dir: %w", err)
	}
	return dir, nil
}

func (d Deps) newActionTemplateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "template",
		Short: "Manage action templates for new platforms",
		Long: `Capture HTML from any URL, generate an ActionDef template via Claude Code skill,
then install it so 'monoagent node run <platform>.<action_type>' works.

Workflow:
  1. monoagent action template capture <url>   # saves rendered HTML to a file
  2. /action-template-generator                # Claude Code skill: reads HTML, writes template JSON
  3. monoagent action template install <file>  # registers template with monoagent`,
	}

	cmd.AddCommand(
		newActionTemplateCaptureCmd(),
		d.newActionTemplateInstallCmd(),
		d.newActionTemplateListCmd(),
	)

	return cmd
}

func newActionTemplateCaptureCmd() *cobra.Command {
	var headless bool
	var waitSecs int
	var outFile string

	cmd := &cobra.Command{
		Use:   "capture <url>",
		Short: "Capture rendered HTML from a URL using a real browser",
		Args:  cobra.ExactArgs(1),
		Example: `  monoagent action template capture https://www.instagram.com/someuser/
  monoagent action template capture https://example.com --headless --wait 5`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]

			parsed, err := url.Parse(rawURL)
			if err != nil {
				return fmt.Errorf("invalid URL %q: %w", rawURL, err)
			}

			domain := strings.ReplaceAll(parsed.Hostname(), ".", "_")
			timestamp := time.Now().Format("20060102_150405")

			captureDir, err := userCaptureDir()
			if err != nil {
				return err
			}

			dest := outFile
			if dest == "" {
				dest = filepath.Join(captureDir, fmt.Sprintf("%s_%s.html", domain, timestamp))
			}

			fmt.Fprintf(os.Stderr, "Launching browser...\n")
			l := launcher.New().
				Headless(headless).
				Set("disable-blink-features", "AutomationControlled")

			launchURL, err := l.Launch()
			if err != nil {
				return fmt.Errorf("launch browser: %w", err)
			}

			browser := rod.New().ControlURL(launchURL)
			if err := browser.Connect(); err != nil {
				return fmt.Errorf("connect to browser: %w", err)
			}
			defer browser.Close()

			page, err := browser.Page(proto.TargetCreateTarget{URL: rawURL})
			if err != nil {
				return fmt.Errorf("open page: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Waiting %ds for JS rendering...\n", waitSecs)
			time.Sleep(time.Duration(waitSecs) * time.Second)

			res, err := page.Eval("() => document.documentElement.outerHTML")
			if err != nil {
				return fmt.Errorf("capture HTML: %w", err)
			}

			html := res.Value.String()
			perm := os.FileMode(0o644) // --output is the user's own file
			if outFile == "" {
				perm = statedir.FilePerm // the capture lands in ~/.monoagent/captures
			}
			if err := os.WriteFile(dest, []byte(html), perm); err != nil {
				return fmt.Errorf("write HTML to %s: %w", dest, err)
			}

			fmt.Printf("Captured: %s\n", dest)
			fmt.Printf("\nNext: run the Claude Code skill to analyze and generate a template:\n")
			fmt.Printf("  /action-template-generator\n")
			fmt.Printf("Point the skill at: %s\n", dest)
			return nil
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false, "Run browser headlessly (no window)")
	cmd.Flags().IntVar(&waitSecs, "wait", 8, "Seconds to wait for JS rendering before capturing")
	cmd.Flags().StringVar(&outFile, "out", "", "Output file path (default: ~/.monoagent/captures/<domain>_<ts>.html)")

	return cmd
}

// newActionTemplateInstallCmd is the deprecated alias kept for one release
// (spec §5.2): the single ActionDef JSON is wrapped into a generated local
// package (id = its automation/platform field) and merged via AddAction.
func (d Deps) newActionTemplateInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "install <file>",
		Short:   "Install an ActionDef JSON (deprecated: use 'action import' or 'automation install')",
		Args:    cobra.ExactArgs(1),
		Example: `  monoagent action template install ~/Downloads/example_scrape_profile.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: 'action template install' is deprecated; use 'monoagentcli action import <file>' (or 'automation install' for packages)\n")
			reg, err := d.Install.OpenRegistry()
			if err != nil {
				return err
			}
			src, cleanup, err := wrapActionJSON(args[0], reg)
			if err != nil {
				return err
			}
			defer cleanup()
			res, err := addActions(reg, src.Manifest.ID, src, automation.InstallOptions{Source: automation.SourceLocal})
			if err = d.Install.WithInstallResult(err, res); err != nil {
				d.Install.PrintFailedIssues(cmd, err)
				return err
			}
			name := src.Manifest.Actions[0]
			// Invalidate loader cache so the next node run picks up the new action.
			actiondef.GetLoader().Invalidate(res.ID, name)
			out := cmd.OutOrStdout()
			if d.Install.JSONOutput() {
				return writeJSONTo(out, res)
			}
			fmt.Fprintf(out, "Installed: %s.%s -> %s\n", res.ID, name, res.Dir)
			fmt.Fprintf(out, "Run it: monoagent node run %s.%s\n", res.ID, name)
			return nil
		},
	}
	d.WithJSONErrors(cmd)
	return cmd
}

// newActionTemplateListCmd lists the actions of the user's own packages
// (trust local or recorded), or of every installed package with --all.
// The node_type/file keys are kept from the pre-package output.
func (d Deps) newActionTemplateListCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the actions of local and recorded automation packages",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := d.Install.OpenRegistry()
			if err != nil {
				return err
			}
			infos, err := reg.List(false)
			if err != nil {
				return err
			}
			type entry struct {
				NodeType   string `json:"node_type"`
				File       string `json:"file"`
				Automation string `json:"automation"`
				Trust      string `json:"trust"`
				Enabled    bool   `json:"enabled"`
			}
			entries := []entry{}
			for _, info := range infos {
				if !all && info.Trust != automation.TrustLocal && info.Trust != automation.TrustRecorded {
					continue
				}
				pkg, err := reg.Get(info.ID)
				if err != nil {
					continue
				}
				for _, name := range pkg.Manifest.Actions {
					entries = append(entries, entry{
						NodeType:   info.ID + "." + name,
						File:       filepath.Join(info.Dir, "actions", name+".json"),
						Automation: info.ID,
						Trust:      info.Trust,
						Enabled:    info.Enabled,
					})
				}
			}
			out := cmd.OutOrStdout()
			if d.Install.JSONOutput() {
				return writeJSONTo(out, entries)
			}
			if len(entries) == 0 {
				fmt.Fprintln(out, "No local or recorded actions. Create a package with: monoagentcli automation new <id>  (or --all for every installed package)")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NODE TYPE\tTRUST\tFILE")
			for _, e := range entries {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.NodeType, e.Trust, e.File)
			}
			tw.Flush()
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include built-in and imported packages")
	d.WithJSONErrors(cmd)
	return cmd
}
