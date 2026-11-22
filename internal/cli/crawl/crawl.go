package crawl

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/integrations"
	"github.com/monoes/mono-agent/internal/statedir"
	"github.com/spf13/cobra"
)

// NewCommand returns `crawl` with its `capture` subcommand.
func NewCommand(d Deps) *cobra.Command {
	var waitSecs int

	cmd := &cobra.Command{
		Use:   "crawl <url>",
		Short: "Capture a website for AI-assisted crawling automation",
		Long: `Open a URL in your own browser through the MonoAgent Bridge extension and capture its HTML for Claude Code analysis.

Claude will then write a monoagent automation package that you can
install and run as a reusable automation node — no API key required.

Steps (automated by Claude when you use /action-template-generator):
  1. monoagentcli crawl <url>                          ← you are here
  2. /action-template-generator                        ← Claude Code skill
  3. monoagentcli automation validate <id>
  4. monoagentcli automation install <id> --dry-run, then ask the operator to run
     "monoagentcli automation install <id> --local" themselves
  5. monoagentcli node run <id>.<action_type>

For the full guide:  monoagentcli ref crawling`,
		Args: cobra.ExactArgs(1),
		Example: `  monoagentcli crawl https://www.producthunt.com/posts/new-product
  monoagentcli crawl https://github.com/someuser
  monoagentcli crawl https://somesite.com/profile --wait 12`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawURL := args[0]

			if waitSecs < 0 || waitSecs > 120 {
				return fmt.Errorf("--wait must be between 0 and 120 seconds, got %d", waitSecs)
			}

			// file://, javascript:, and friends would point the browser at local or
			// non-web content; only plain web pages make sense to crawl.
			parsed, err := clicore.WebURL(rawURL)
			if err != nil {
				return err
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve home dir: %w", err)
			}
			captureDir := filepath.Join(home, ".monoagent", "captures")
			if err := statedir.MkdirAll(captureDir); err != nil {
				return fmt.Errorf("create capture dir: %w", err)
			}
			htmlFile := filepath.Join(captureDir, clicore.CaptureFileName(parsed.Hostname(), time.Now()))

			fmt.Fprintf(os.Stderr, "Opening %s in your browser through the extension...\n", parsed.Hostname())
			html, err := d.RenderHTML(cmd.Context(), rawURL, time.Duration(waitSecs)*time.Second)
			if err != nil {
				return err
			}
			if err := os.WriteFile(htmlFile, []byte(html), statedir.FilePerm); err != nil {
				return fmt.Errorf("save HTML: %w", err)
			}

			// Check whether the Claude skill is installed.
			claudeDir := filepath.Join(home, ".claude")
			skillInstalled := false
			if _, err := os.Stat(integrations.ClaudeSkillFile(filepath.Join(claudeDir, "skills"), integrations.ClaudeSkillNames[0])); err == nil {
				skillInstalled = true
			}

			if d.JSONOutput() {
				return clicore.WriteJSONTo(cmd.OutOrStdout(), map[string]any{
					"url": rawURL, "html_file": htmlFile, "html_size": len(html), "skill_ready": skillInstalled,
				})
			}

			// Print structured output Claude can read and act on.
			fmt.Printf("\n[MONOAGENT_CRAWL]\n")
			fmt.Printf("url:       %s\n", rawURL)
			fmt.Printf("html_file: %s\n", htmlFile)
			fmt.Printf("html_size: %d bytes\n", len(html))
			fmt.Printf("skill_ready: %v\n", skillInstalled)
			fmt.Printf("[/MONOAGENT_CRAWL]\n\n")

			if skillInstalled {
				fmt.Printf("Next — run the Claude Code skill:\n")
				fmt.Printf("  /action-template-generator\n\n")
				fmt.Printf("When prompted, provide the HTML file path:\n")
				fmt.Printf("  %s\n\n", htmlFile)
				fmt.Printf("After the skill writes the package, validate and install it:\n")
				fmt.Printf("  monoagentcli automation validate <id>\n")
				fmt.Printf("  monoagentcli automation install <id> --dry-run\n")
				fmt.Printf("Then ask the operator to run, in their own terminal:\n")
				fmt.Printf("  monoagentcli automation install <id> --local\n")
			} else {
				fmt.Printf("Claude skill not installed. Run once:\n")
				fmt.Printf("  monoagentcli init --claude\n\n")
				fmt.Printf("Then run:\n")
				fmt.Printf("  /action-template-generator\n")
				fmt.Printf("  (provide html_file: %s)\n", htmlFile)
			}

			return nil
		},
	}

	cmd.AddCommand(d.newCrawlCaptureCmd())

	cmd.Flags().IntVar(&waitSecs, "wait", 8, "Seconds to wait for JS rendering")

	return cmd
}
