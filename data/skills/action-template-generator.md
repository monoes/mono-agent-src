---
name: action-template-generator
description: Generate a monoagent browser automation package (automation.json, actions/*.json, selectors.json) from captured HTML. Analyzes page structure using Claude's built-in reasoning to produce declarative step definitions that can be installed and run without any API key. Invoke when the user wants to crawl, scrape, or automate a new website or social media platform not already supported by monoagent.
---

# Action Template Generator

You are writing a **monoagent automation package** for a website by analyzing captured HTML. The package holds declarative actions that monoagent's browser automation engine executes. No AI is needed at runtime.

Official packages from monoes.me already cover instagram, linkedin, x, tiktok, hackernews, producthunt and gemini (`monoagentcli library list --kind automation`). Check there before writing a new one. The full guide ships with the binary: `monoagentcli ref crawling`.

## Context

This skill is invoked after the user runs:
```bash
monoagentcli action template capture <url>   # [--wait <secs>] [--headless] [--out <file>]
# or
monoagentcli crawl <url>
```

That command opens a real browser, renders the page (including JavaScript), and saves the DOM to `~/.monoagent/captures/<domain>_<timestamp>.html`.

## Your task

1. Ask the user for the captured HTML file path (printed by the capture command)
2. Read the file with the Read tool
3. Analyze the HTML structure to identify the data elements and controls
4. Scaffold a package, unless the user already has one:
   `monoagentcli automation new <id> --template <basic|list-scrape|search-and-extract|login-form|post-content> --start-url <url>`
5. Write the action into `<id>/actions/<action_type>.json` and its selectors into `<id>/selectors.json`. List the action in `automation.json` `actions`, and every step type it uses in `permissions.steps`
6. Run `monoagentcli automation validate <id>` and fix every error it reports
7. Tell the user how to install and run it (see Output Instructions)

---

## Package layout

```
<id>/
  automation.json      manifest: id, site.startUrl, site.domains, permissions.steps, actions[]
  actions/<name>.json  one ActionDef per action
  selectors.json       configKey → ranked selector candidates + intent
  fragments/<name>.json  reusable step lists (call_fragment)
  README.md
```

`navigate` URLs must stay inside `site.domains`.

## ActionDef

```json
{
  "$schema": "https://raw.githubusercontent.com/monoes/mono-agent/master/data/schemas/action.v1.schema.json",
  "actionType": "scrape_profile_info",
  "automation": "<id>",
  "version": "1.0.0",
  "description": "Read name, bio and follower count from a profile page.",
  "sideEffects": "read",
  "inputs": {
    "required": [
      { "name": "url", "type": "string", "description": "Profile URL to read" }
    ]
  },
  "outputs": { "success": ["name", "bio", "followers"], "failure": ["error"] },
  "steps": [
    { "id": "open", "type": "navigate", "url": "{{url}}", "timeout": 20 },
    { "id": "loaded", "type": "wait_for", "until": { "selector": "h1" }, "timeout": 15 },
    { "id": "name", "type": "extract_text", "configKey": "profile.name",
      "variable_name": "name", "intent": "the person's display name" },
    { "id": "bio", "type": "extract_text", "configKey": "profile.bio",
      "variable_name": "bio", "intent": "the profile bio text",
      "onError": { "action": "skip" } },
    { "id": "followers", "type": "extract_text", "configKey": "profile.followers",
      "variable_name": "followers", "intent": "the follower count" }
  ]
}
```

- **Required fields:** `actionType` and `steps`. Every step needs a unique `id`.
- **Inputs:** referenced by name: `{{url}}`, `{{query}}`. The manifest's start URL is `{{startUrl}}`.
- **Results:** a step stores its result in `variable_name`, and later steps read it as `{{name}}`.
- **`sideEffects`:** `read` for actions that only read. Mark any step that writes, sends or deletes on the site with `"sideEffect": true`.

## selectors.json

Put selectors here, keyed by `configKey`, instead of inline in the step. They can then be repaired later with `automation rerecord <id> <key>` or a Jev repair, without editing the action:

```json
{
  "profile.name": {
    "candidates": [
      { "css": "[data-testid='UserName'] h1", "score": 0.9 },
      { "aria": { "role": "heading", "name": "Profile" }, "score": 0.7 },
      { "xpath": "//main//h1", "score": 0.5 }
    ],
    "intent": "the person's display name"
  }
}
```

Candidates are tried highest score first. A candidate is one of `css`, `xpath`, `aria {role, name}` or `text`. The `intent` is a plain description of the element. When every candidate misses, monoagent's Jev fallback, if the user enabled it, uses the intent to find the element.

## Step types

These are the most useful steps. The full list is in `data/schemas/action.v1.schema.json`, and `automation validate` checks it:

| Step | Use |
|---|---|
| `navigate` | `url` (template); stays inside `site.domains` |
| `wait_for` | `until: {selector}`, `{text}` or `{any: [...]}`; `timeout` in seconds |
| `wait` | `duration` in seconds |
| `extract_text` / `extract_attribute` | `configKey`, `variable_name`, (`attribute`) |
| `extract_table` | rows → records: `value: {rows: ".item"}`, `fields: {"title": "a.title", "url": "a.title@href"}` |
| `extract_json` | read a JSON value at `path` |
| `click`, `type`, `press_key`, `hover`, `scroll`, `select_option` | interact; `type` takes `value`, `press_key` takes `key` ("Enter") |
| `for_each` | `items`, `as`, inline `steps` |
| `condition` | `condition` expression, `then` / `else` step ids |
| `set_variable`, `transform` | shape data (`ops`: `dedupe`, `limit`, …) |
| `call_fragment` | run `fragments/<name>.json` (e.g. a cookie banner) |
| `save_data` | `dataSource`: the variable to emit as the run output |
| `mark_failed`, `log`, `assert` | control and diagnostics |

**Never use `call_bot_method`**: it only works for the compiled-in platforms. `page_script` and `http_fetch_in_page` need the package to be trusted (`automation trust`). Avoid them unless the page offers no other way.

**Per-step errors:** `"onError": {"action": "skip"}`. The other actions are `retry`, `mark_failed` and `abort`.

---

## Selector rules

1. **Never rely on generated class names**, which change on every deploy. Prefer `data-testid`, `aria-label`, `role`, `id`, `name`
2. **One element per selector**: each candidate must match exactly one element (rows go through `extract_table`)
3. **No hardcoded content**: no usernames, specific ids or text values from the captured page
4. **Structural over positional**: `main h1` beats `h1:nth-of-type(3)`
5. **Several candidates per key**, best first, each with an honest `score` between 0 and 1, and always an `intent`

---

## Output Instructions

1. Read the HTML file
2. Detect the site from its domain or `<meta property="og:site_name">`
3. Identify the data elements (name, bio, counts, avatar, links, list rows) and the controls the action needs
4. Write the action and `selectors.json` following the rules above
5. Run `monoagentcli automation validate <id>` and fix what it reports
6. Print:
```
Package written: <dir>/<id>

Validate: monoagentcli automation validate <id>
Install:  monoagentcli automation install <id> --local --yes
Run:      monoagentcli node run <id>.<action_type> --config '{"url":"<page url>"}'
```

To merge a single loose action file into an installed automation instead, use `monoagentcli action import <file>.json --into <id> --yes`. `action template install` is deprecated.
