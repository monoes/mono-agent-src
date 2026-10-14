package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

func refConnectionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connections",
		Short: "Profiles, OAuth connections, credential resolution, and account identity",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║   monoagentcli — profiles, connections & account identity      ║
╚══════════════════════════════════════════════════════════════╝

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
PROFILES — the isolation boundary
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

A profile fully isolates people, messages, connections, and workflows
from every other profile. There's always a "default" profile; create
more with 'monoagentcli profile create <name>'.

  monoagentcli profile list                 list profiles + which is active
  monoagentcli profile create <name>        make a new one
  monoagentcli profile switch <name>        change the active default
  monoagentcli --profile <name-or-id> ...   override for a single command

--profile accepts EITHER a profile's display name OR its ID — resolved to
the canonical ID before anything runs. Passing a name that matches no real
profile errors out; it does not silently fall back to "default".

Every read/write in this CLI is scoped to a profile — mostly the ACTIVE
one, overridable per-command with --profile. When writing a NEW workflow
node by hand (not via 'workflow templates use', which already does this),
double-check any node that takes its own "profile_id" config field (e.g.
people.sync_outlook_message) — it does NOT inherit the workflow's profile
automatically and defaults to "default" if you leave it unset. See
'monoagentcli ref node people.sync_outlook_message' for the specific gotcha.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
CONNECTIONS — one external account, one row
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

A "connection" is one authenticated external account (Outlook, Slack,
GitHub, ...), always scoped to exactly one profile. Two profiles can each
have their own connection for the SAME platform (e.g. two different
Outlook accounts) — nothing links them beyond both being "outlook".

  monoagentcli connect <platform>               interactive connect (OAuth or API key)
  monoagentcli connect list                     connections for the ACTIVE profile
  monoagentcli connect list --all               every platform this CLI supports, connected or not
  monoagentcli connect test <id>                verify a connection still works
  monoagentcli connect refresh <id>             force a silent token refresh right now
  monoagentcli connect remove <id>              delete a connection

'connect list' shows an ACCOUNT column with the REAL resolved identity for
platforms that support it (currently Outlook) — e.g. the actual mailbox
address, not just the connection's human-supplied label. Resolved once at
connect/refresh/test time via the smallest possible read on that platform
(for Outlook: one field of one message — no extra OAuth scope, no message
content). A profile named "acme" is a LABEL, not a guarantee that the
acme.com account is what actually got authenticated — always sanity-check
the ACCOUNT column (or run the platform's whoami node op, e.g.
'monoagentcli node run service.outlook_mail --credential <id> --config
"{\"operation\":\"whoami\"}"') before trusting a profile's data as
belonging to a specific real-world account. 'connect <platform>' and
'connect refresh' both print a non-blocking warning to stderr if the
resolved account doesn't obviously relate to the profile's name.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
--credential: EXACT CONNECTION ID vs PLATFORM NAME
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Every service.* node that needs auth takes credential_id (in --config) or
the 'monoagentcli node run ... --credential <value>' flag. That value can
be:

  • an EXACT connection ID (from 'connect list')      — unambiguous, always
                                                          correct, verified
                                                          to belong to the
                                                          active --profile
  • a PLATFORM NAME (e.g. "outlook", "slack")          — resolved to that
                                                          platform's active
                                                          connection WITHIN
                                                          THE ACTIVE PROFILE
                                                          ONLY

Prefer the exact connection ID in scripts, agents, or workflow node
configs that must be unambiguous — especially once more than one profile
has a connection for the same platform. (Historical note: before a
2026-07 fix, the platform-name form ignored --profile entirely and always
matched the OLDEST connection for that platform across every profile —
already fixed, but a good reason to default to exact IDs when precision
matters.)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OAUTH TOKEN REFRESH — silent, automatic, per-profile client credentials
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

OAuth access tokens expire (typically ~1h). Every credential-resolution
path (CLI node run, the GUI, the daemon's workflow triggers) silently
refreshes an expiring token using its stored refresh_token before use —
no user action needed in the common case.

Silent refresh needs the OAuth app's client_id (and client_secret, if the
app isn't a public/native client). Store it once per platform+profile:

  monoagentcli connect set-oauth-client <platform> --client-id <id> [--client-secret <secret>]

Two profiles' connections for the same platform CAN use different Azure/
OAuth app registrations (e.g. a personal-account-only app for one profile,
a work/school-only app for another) — set-oauth-client is scoped per
profile for exactly this reason.

If silent refresh fails (e.g. missing client credentials, revoked
consent), the affected command falls back to using the existing —
possibly expired — token rather than blocking, and prints a warning. Fix
with set-oauth-client, or 'monoagentcli connect refresh <id>' /
'monoagentcli connect <platform>' to fully re-authenticate.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
OAUTH CLIENT IDS — you bring your own
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

The binary ships no OAuth client id for any platform. Before the first
'connect <platform>' on an OAuth platform, register an OAuth app with
that platform yourself and give monoagent its client id, in either way:

  MONOAGENT_<PLATFORM>_CLIENT_ID (and _CLIENT_SECRET) in the environment
  monoagentcli connect set-oauth-client <platform> --client-id <id>

The stored per-profile value wins over the environment. A successful
connect stores the client it used, for silent refresh later.

The monoes.me OAuth app is for signing in to monoes.me (see 'ref
account'), not for connecting integrations. Reusing it for integrations
is a possible future option, considered only if it blocks nothing and
cannot be abused. It is not implemented.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
SCHEDULED/WEBHOOK TRIGGERS NEED THE DAEMON RUNNING
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

'monoagentcli workflow activate <id>' only keeps trigger.schedule /
trigger.webhook nodes live for as long as THAT command keeps running.
For a workflow to actually fire on its own over time (e.g. an hourly
sync), 'monoagentcli daemon' must be running continuously — it restores
and keeps alive every active workflow's triggers across ALL profiles.
See 'monoagentcli daemon --help'.
`)
		},
	}
}
