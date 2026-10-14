package main

// The `library` entries of `ref commands` (kept apart from ref.go, which is
// long enough).
func init() {
	cliDocs = append(cliDocs,
		cmdDoc{
			Name:  "library login",
			Short: "Log in to monoes.me (browser with PKCE, or a code by email); list, show, install and update need it",
			Usage: "monoagentcli library login [--no-browser] [--timeout 5m] | --email <addr> [--send | --code <code>]",
			Flags: `  --no-browser        Print the sign-in URL instead of opening the browser
  --timeout duration  How long to wait for the browser (default 5m)
  --email string      Log in with a code sent to this address
  --send              With --email: only send the code
  --code string       With --email: the code from the email`,
			Examples: []string{
				"monoagentcli library login",
				"monoagentcli library login --email you@example.com --send",
				"monoagentcli library login --email you@example.com --code 123456",
				"monoagentcli library status",
				"monoagentcli library logout",
			},
		},
		cmdDoc{
			Name:  "library list",
			Short: "Browse monoes.me: official, public (community) and your own items (needs a login; exit 4 without)",
			Usage: "monoagentcli library list [--kind workflow|automation|org|api] [--scope public|official|mine] [--search q] [--tag t]",
			Examples: []string{
				"monoagentcli library login",
				"monoagentcli library list --kind automation --scope official",
				"monoagentcli library list --kind api",
				"monoagentcli --json library list --scope mine",
				"monoagentcli library show automation/instagram",
				"monoagentcli library installed",
			},
		},
		cmdDoc{
			Name:  "library install",
			Short: "Download a library item, verify its sha256 and install it (needs a login; exit 4 without). An API package is validated, then written to ~/.monoagent/apipkgs/<id>/package.json: its app.<id> node is available to the next command",
			Usage: "monoagentcli library install <workflow|automation|org|api> <id|slug> [--yes] [--dry-run] [--replace] [--rename <name>]",
			Examples: []string{
				"monoagentcli library install automation hackernews",
				"monoagentcli library install workflow gemini-generate-one-image",
				"monoagentcli library install org research-team --rename research-2",
				"monoagentcli library install api trello",
				"monoagentcli library update --dry-run",
			},
		},
		cmdDoc{
			Name:  "library publish",
			Short: "Upload a workflow, automation, org or API package to your monoes.me library (private unless --public)",
			Usage: "monoagentcli library publish <workflow|automation|org|api> <local id | API package file> [--public] [--name] [--description] [--tags a,b] [--version v] [--no-bundle] [--new]",
			Examples: []string{
				"monoagentcli library publish workflow 3f1c… --public --tags email,crm",
				"monoagentcli library publish org growth-team",
				"monoagentcli library publish automation my-site",
				"monoagentcli library publish api acme            # the installed ~/.monoagent/apipkgs/acme",
				"monoagentcli library publish api ./acme/package.json --public",
			},
		},
	)
}
