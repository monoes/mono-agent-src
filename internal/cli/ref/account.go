package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

const refAccountText = `monoagentcli — monoes.me account

CURRENT BUILD
  The account gate is live. A monoes.me sign-in has been required since
  2026-10-11 (UTC); account status shows the date as enforce_from. A gated
  command without a valid sign-in exits 4 with login_required and does nothing
  else. The online library requires a sign-in too. Releases are published on
  monoes.me.
  The production signing key is pinned: a production machine session is
  verified on this machine, with no network call per command.

COMMANDS
  monoagentcli account login
  monoagentcli account login --no-browser --timeout 2m
  monoagentcli account login --email you@example.com
  monoagentcli account login --email you@example.com --send
  monoagentcli account login --email you@example.com --code <code>
  monoagentcli account status [--offline] [--json]
  monoagentcli account logout
  library login, library logout and library status manage the same machine
  session. status --offline reads locally without refreshing. An explicit
  status exits 4 when the sign-in is locked (for example not_logged_in) and 0
  while the session is good, including offline grace. status --json prints {"v":1,"state","reason",
  "user","plan","issued_at","valid_until","grace_until","enforce_from","enforced"}:
  valid_until is the access token's expiry (about an hour), grace_until its issue
  time plus 24 hours, and enforce_from is the enforcement date, 2026-10-11 UTC. Never paste
  a token into a command, workflow or chat; ask the user to run account login.
  No credential comes from an environment variable, and there are no unattended
  machine tokens.

STORAGE AND NETWORK
  ~/.monoagent/account/ (0700): session.json, refresh.enc, session.lock (0600).
  One session per OS user, shared by profiles regardless of --db-path.
  refresh.enc is sealed under the OS key store (or the file keyring fallback).
  Sign in separately on every machine. Never copy or restore this folder, even
  from an old backup, or bake it into an image: monoes.me treats a refresh token
  that was already used as stolen and ends that sign-in, so a stale copy, once
  presented, signs out the original and every copy. Machines that signed in on
  their own keep working. Signing out of the monoes.me website does not sign out
  an install. In a file keyring, secret keyring status lists the sign-in's key
  as monoes..account once an explicit account login has created it.
  Default account commands use https://monoes.me; another sign-in host requires
  -tags devaccount. The library still honors MONOES_BASE_URL for its requests,
  but sends a machine session only to its issuing host.
  monoes.me receives the client id, sign-in proof or refresh token, requested
  resource/scopes, and with --email the address you type. It sees the account,
  request time and source IP address. No device identifier or usage counters
  are sent by the account gate. SECURITY.md lists the network requests.

STATES
  enforced: since enforce_from, locked gated commands refuse before doing work.
  ok: the signed token is valid. grace: renewal failed but the token is still
  valid, at most 24 hours from its signed issue time. locked: work is refused.
  Reasons include not_logged_in, expired, refused, clock_rollback, clock_skew,
  key_unknown (update), invalid, unreachable, server_error, keyring_unavailable
  and unconfirmed. Only invalid_grant in response to a refresh is refusal;
  other failures preserve grace while the token remains valid. An unknown
  refresh outcome is retried for 240 seconds; this machine then drops its
  refresh token and becomes unconfirmed. Sign in again on this machine.
  Clock rollback can lock work; a freshly verified token resets the clock guard.

WHAT STAYS OPEN
  Open: version, help, completion (including __complete and __completeNoDesc),
  ref, update, doctor (including doctor fix), setup, release (maintainer tools),
  account (all subcommands), library login, library logout, library status.
  doctor has a core.monoes_account row (fix: account login).
  Serving: daemon, httpapi, mcp (including --grant), extension serve (also the
  bridge serve alias). They start while locked and refuse work; they stay up
  so service managers do not repeatedly respawn them.
  Every other command is gated, including local-only commands, daemon install,
  daemon restart, daemon uninstall and org serve.

REFUSAL
  Exit 4; first line: Log in to monoes.me first: monoagentcli account login
  With --json, login_required is true and code is auth_or_connection.
  Branch on exit 4 and login_required, not on message wording.
  HTTP API and /v1: 401 login_required. GET /health stays open and reports the
  account. Webhooks: 503 without execution. Extension bridge: account_locked.
  MCP: tool error. A refused workflow run records FAILED / login_required:.
  A locked daemon stays up, starts no new work, stops its org services and
  resumes when a valid sign-in appears. The Docker version health check only
  tests liveness, not permission to work.

HEADLESS AND DOCKER
  Sign in with account login --email as the OS user running the daemon.
  Docker HOME=/data keeps /data/.monoagent/account/ in the persistent volume.
  A container has no OS keychain: enable MONOAGENT_ALLOW_FILE_KEYRING=1 and
  create a private passphrase file BEFORE login. Set
  MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE to it (or use
  ~/.monoagent/keyring-passphrase). README's Docker section has the commands.
  A daemon has no terminal. Without a passphrase source it cannot unseal its
  refresh token: the gate reports grace / keyring_unavailable, then
  locks at token expiry (at most 24 hours). secret keyring set-passphrase is
  itself gated. No account credential comes from an environment variable;
  there are no unattended machine tokens.

DEVELOPERS
  -tags devaccount trusts a development signing key and permits a development
  host via MONOES_BASE_URL. MONOAGENT_DEV_ENFORCE_FROM is read only by that
  build to move the enforcement date in a test. Releases never carry devaccount.
  The license (LICENSE, section 4) forbids bypassing the check; the account gate is a product check,
  not a security boundary against the OS user who can rebuild the source.
`

func refAccountCmd() *cobra.Command {
	return &cobra.Command{
		Use: "account", Short: "Machine sign-in, the account gate, and headless setup",
		Args: cobra.NoArgs,
		Run:  func(cmd *cobra.Command, args []string) { fmt.Fprint(cmd.OutOrStdout(), refAccountText) },
	}
}
