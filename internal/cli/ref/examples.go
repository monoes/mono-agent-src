package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ── ref examples ──────────────────────────────────────────────────────────────

func refExamplesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "examples",
		Short: "Common workflow patterns and use cases",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║               monoagentcli — common workflow examples              ║
╚══════════════════════════════════════════════════════════════╝

──────────────────────────────────────────────────────────────
1. DAILY NEWS → PERSIAN TRANSLATION → INSTAGRAM POST
──────────────────────────────────────────────────────────────
Nodes (in order):
  trigger.schedule      cron: "0 0 9 * * *"
  system.rss_read       url: DW/Spiegel/Zeit feeds  (3 parallel nodes)
  core.merge            combine all feed items
  core.aggregate        collect titles + descriptions into arrays
  core.set              build Gemini prompt string
  gemini.generate_text  translate/summarize to Persian
  core.set              build short English image prompt
  gemini.generate_image generate news collage image
  core.set              extract media_path + build caption
  instagram.publish_post upload image + caption

Key tips:
  - Image prompt must be SHORT English text (< 100 chars)
  - Use replace to inject \n\n around separators in Gemini output
  - media in instagram.publish_post must be a native JSON array

──────────────────────────────────────────────────────────────
2. BULK INSTAGRAM DM CAMPAIGN
──────────────────────────────────────────────────────────────
  monoagentcli --profile work login instagram
  monoagentcli --profile work people import leads.csv
  monoagentcli message instagram --list leads --text "Hi {{name}}! ..."

Or as a workflow:
  trigger.schedule → people.lookup → instagram.send_dms

──────────────────────────────────────────────────────────────
3. INSTAGRAM ENGAGEMENT LOOP
──────────────────────────────────────────────────────────────
  monoagentcli --profile work login instagram
  monoagentcli run ENGAGE_WITH_POSTS --platform instagram --list target-posts

Or workflow:
  trigger.schedule → instagram.find_by_keyword → instagram.engage_with_posts

──────────────────────────────────────────────────────────────
4. RSS → SLACK DIGEST
──────────────────────────────────────────────────────────────
  trigger.schedule   →  system.rss_read  →  core.limit (5)
    →  core.aggregate (titles into array)
    →  core.set (build Slack message)
    →  comm.slack

──────────────────────────────────────────────────────────────
5. WEB SCRAPE → GOOGLE SHEETS
──────────────────────────────────────────────────────────────
  trigger.schedule  →  http.request  →  ai.extract_page
    →  service.google_sheets (append rows)

──────────────────────────────────────────────────────────────
6. TEST A SINGLE NODE QUICKLY
──────────────────────────────────────────────────────────────
  monoagentcli --profile work node run system.rss_read \
    --config '{"url":"https://rss.dw.com/rdf/rss-en-all","limit":3}'

  # Gemini nodes use BROWSER AUTOMATION — no API key required.
  # Run 'monoagentcli login gemini' once to authenticate, then:
  monoagentcli --profile work node run gemini.generate_text \
    --config '{"prompt":"Say hello in Persian"}'

  # Standard text-to-image generation:
  monoagentcli --profile work node run gemini.generate_image \
    --config '{"prompt":"sunset over a mountain lake","downloadDir":"~/.monoagent/downloads"}'

  # Style transfer — upload a reference image and ask Gemini to recreate it
  # in a different style. referenceImagePath is uploaded BEFORE the prompt is sent.
  monoagentcli --profile work node run gemini.generate_image \
    --config '{
      "prompt": "recreate this in a watercolor painting style with soft pastel colors",
      "referenceImagePath": "/path/to/my-photo.jpg",
      "downloadDir": "~/.monoagent/downloads"
    }'

──────────────────────────────────────────────────────────────
7. CRAWL A NEW WEBSITE / CUSTOM PLATFORM (Claude Code integration)
──────────────────────────────────────────────────────────────
Use this when you want to automate scraping on any site that does NOT
have an official automation package on monoes.me (instagram, linkedin, x,
tiktok, hackernews, producthunt, gemini — log in with
` + "`monoagentcli library login`" + `, then install those with
` + "`monoagentcli library install automation <id>`" + `).

Step 1 — capture the rendered page HTML (real browser, JS executed):
  monoagent action template capture https://somesite.com/someuser
  # Saves to: ~/.monoagent/captures/somesite_com_<timestamp>.html

Step 2 — scaffold an automation package and write the action:
  monoagent automation new <id> --template list-scrape --start-url https://somesite.com/
  In Claude Code, /action-template-generator reads the captured HTML and
  writes the action JSON into the package's actions/ folder.
  No API key required — Claude's built-in reasoning does the analysis.
  (A single loose action file: monoagent action import <file>.json)

Step 3 — validate and install the package:
  monoagent automation validate <id>
  monoagent automation install <id> --yes

Step 4 — run it like any built-in node:
  monoagent node run <id>.<action_type>

List installed automations:
  monoagent automation list

Full guide:  monoagentcli ref crawling

──────────────────────────────────────────────────────────────
8. TROUBLESHOOTING CHECKLIST
──────────────────────────────────────────────────────────────
  "workflow not found"
    → check the id with 'workflow list'; a file dropped into ~/.monoagent/workflows/ is not a workflow
      until it is imported ('workflow import --file') or adopted ('doctor')

  "invalid cron spec"
    → use 6-field cron: "0 0 9 * * *"  (sec min hour day month weekday)

  "I need a Gemini API key"
    → WRONG. monoagentcli uses BROWSER AUTOMATION, not the API. No key needed.
    → Run: monoagentcli login gemini   (one-time, opens Chrome for Google login)
    → Then use gemini.generate_image with just "prompt" — no credential_id required.

  Gemini images not generating
    → use short English prompts (< 100 chars); long/Persian prompts trigger reasoning mode
    → check session is alive: monoagentcli login status

  Generating a Gemini image that looks like an existing photo (style transfer)
    → set "referenceImagePath" to the local path of your image
    → monoagent uploads the image to Gemini BEFORE sending the prompt
    → write your prompt to describe the style: "recreate this in oil painting style"
    → example:
        monoagentcli --profile work node run gemini.generate_image \
          --config '{"prompt":"recreate this as a vintage poster","referenceImagePath":"/Users/me/photo.jpg"}'
    → referenceImagePath is OPTIONAL — omit it for standard text-to-image generation

  Instagram posting as comment instead of new post
    → media must be a native JSON array: [{"url": "{{ $json.media_path }}"}]
    → ensure media_path is a plain string field set by core.set first

  Extension port conflict
    → only one monoagentcli process at a time (CLI and Wails share port 9222)

`)
		},
	}
}
