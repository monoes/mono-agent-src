package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

func refCrawlingCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "crawling",
		Short: "How to automate scraping on new or custom platforms",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║        monoagentcli — crawling new platforms with Claude Code      ║
╚══════════════════════════════════════════════════════════════╝

Official automation packages on monoes.me cover instagram, linkedin, x,
tiktok, hackernews, producthunt and gemini (log in with monoagentcli
library login, then monoagentcli library list --kind automation; install
one with monoagentcli library install automation <id>). For any other
website, use the action template
workflow below.
No API keys required — Claude Code's built-in reasoning does the analysis.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
THE WORKFLOW
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

STEP 1  Capture the rendered HTML
──────────────────────────────────
  monoagent action template capture <url>

  Options:
    --wait <secs>   Wait for JS rendering (default: 8s)
    --headless      Run browser invisibly
    --out <file>    Custom output path

  Example:
    monoagent action template capture https://www.producthunt.com/posts/new-product
    # Saves to: ~/.monoagent/captures/www_producthunt_com_20240506_143022.html

STEP 2  Generate a template with Claude Code
─────────────────────────────────────────────
  In Claude Code, run the skill:
    /action-template-generator

  The skill will:
    • Ask for the HTML file path from Step 1
    • Analyze DOM structure to identify data elements
    • Build XPath selectors (no @class — stable across deployments)
    • Output a valid ActionDef JSON template
    • Save it in an automation package's actions/ folder — scaffold one
      first with: monoagent automation new <id> --template basic --start-url <url>

STEP 3  Install it
───────────────────
  monoagent automation validate <dir>       # schema + lint + fixture tests
  monoagent automation install <dir> --yes  # installs into ~/.monoagent/automations

  A single loose action file merges into an automation with:
    monoagent action import <file>.json [--into <id>] --yes

  Example:
    monoagent automation new producthunt-extra --template basic --start-url https://www.producthunt.com/
    # write actions/scrape_post.json, then:
    monoagent automation install producthunt-extra --yes

STEP 4  Run it
───────────────
  monoagent node run <id>.<action_type>

  Example:
    monoagent node run producthunt-extra.scrape_post \
      --config '{"url":"https://www.producthunt.com/posts/some-product"}'

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
MANAGE INSTALLED TEMPLATES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  monoagent automation list             # installed automation packages
  monoagent action template list        # local and recorded packages' actions
  monoagent node list                   # includes installed actions

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
ACTIONDEF ALLOWED STEP TYPES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  navigate         Go to a URL and wait for it to load
  wait             Sleep for N milliseconds
  find_element     Locate an element by XPath, store as variable
  extract_text     Extract visible text from an element
  extract_attribute  Extract an HTML attribute value
  extract_multiple Extract a list of elements with multiple fields
  click            Click an element
  scroll           Scroll the page
  type             Type text into an input
  hover            Hover over an element
  condition        Branch on a variable value
  log              Emit a debug log message
  save_data        Persist extracted fields to the run output
  mark_failed      Mark this target as failed with a reason

  ⚠ DO NOT use: call_bot_method — only works for compiled-in platforms.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
XPATH RULES (automatically enforced by the skill)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  • Never use @class (changes on every deploy)
  • Use @data-testid, @aria-label, @role, @id, @name
  • Each XPath must match exactly one element
  • Do not hardcode usernames, IDs, or text content
  • Prefer structural/semantic attributes over position

`)
		},
	}
}
