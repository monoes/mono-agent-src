package main

// The `account` entries of `ref commands` (kept apart from ref.go, which is
// long enough).
func init() {
	cliDocs = append(cliDocs,
		cmdDoc{
			Name:  "account login",
			Short: "Sign in to monoes.me (browser with PKCE, or a code by email); required since 2026-10-11 (UTC)",
			Usage: "monoagentcli account login [--no-browser] [--timeout 5m] | --email <addr> [--send | --code-stdin | --code <code>]",
			Flags: `  --no-browser        Print the sign-in URL instead of opening the browser
  --timeout duration  How long to wait for the browser (default 5m)
  --email string      Sign in with a code sent to this address (a machine with no browser)
  --send              With --email: only send the code
  --code-stdin        With --email: read the code from stdin, so it never shows in the process list
  --code string       With --email: the code from the email (visible in the process list)`,
			Examples: []string{
				"monoagentcli account login",
				"monoagentcli account login --email you@example.com --send",
				"echo 123456 | monoagentcli account login --email you@example.com --code-stdin",
			},
		},
		cmdDoc{
			Name:     "account status",
			Short:    "Show the monoes.me sign-in: state, reason, who, and when it stops working (exit 0 for ok and grace, 4 for locked)",
			Usage:    "monoagentcli account status [--offline] [--json]",
			Flags:    `  --offline           Do not refresh first; report what is stored`,
			Examples: []string{"monoagentcli account status", "monoagentcli account status --offline --json"},
		},
		cmdDoc{
			Name:     "account logout",
			Short:    "Revoke the refresh token at monoes.me (best effort) and delete the local sign-in",
			Usage:    "monoagentcli account logout",
			Examples: []string{"monoagentcli account logout"},
		},
	)
}
