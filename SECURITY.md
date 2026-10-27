# Security Policy

Mono Agent is a workflow automation tool that runs on your machine. This document explains
how to report vulnerabilities, what is in scope, and how your data is handled.

## Reporting a Vulnerability

Please report vulnerabilities privately — do **not** open a public GitHub issue.

- **GitHub private vulnerability reporting** (preferred):
  https://github.com/monoes/mono-agent/security/advisories/new
- **Email**: security@monoes.me

Include reproduction steps, affected commands/versions, and impact assessment
where possible. We aim to acknowledge reports within 72 hours and will
coordinate a fix and disclosure timeline with you. Good-faith research into
the security of this project is welcomed; we will not pursue legal action
against reporters who avoid service degradation, data exfiltration, and
privacy violations.

## Supported Versions

| Version | Supported |
| ------- | --------- |
| latest release | ✅ |
| older releases | ❌ — please update before reporting |

## Scope

Mono Agent is a desktop/CLI automation platform that runs on your machine
and signs in to monoes.me. In scope:

- The `monoagentcli` binary and the Go packages under `internal/`
- The workflow engine, node implementations, and CLI surface
- The MCP server (stdio JSON-RPC) for AI agents
- The encrypted secrets vault (see below)
- The assistant chat tool surface (`monoagentcli chat --tools` — see
  [Assistant tools](#assistant-tools-chat---tools) below)
- The browser-extension bridge and bundled deployment scripts
- The monoes.me session (`internal/account`: token verification, the sign-in on
  disk, the gate) and what it sends ([Network use](#network-use-telemetry-and-crash-reporting))

Out of scope:

- Vulnerabilities in third-party services Mono Agent can connect to
  (report those to the service operator)
- Issues in user-authored workflows or user-installed action templates
- Credential leaks caused by how a user configured their own environment
- Removing the sign-in requirement from a binary you built or patched yourself
  ([a product check](#the-sign-in-requirement-is-a-product-check))

## Secrets Vault

Credentials (API keys, OAuth tokens, website logins) are stored in an
encrypted vault, never in plaintext on disk. The design is described in
[docs/superpowers/specs/2026-07-13-secrets-vault-design.md](docs/superpowers/specs/2026-07-13-secrets-vault-design.md)
and implemented in [`internal/secrets`](internal/secrets):

- Data is encrypted with AES-256-GCM using per-profile data-encryption keys
- Key-encryption keys are backed by the OS keyring (macOS Keychain,
  Windows Credential Manager, Linux Secret Service) via go-keyring
- Plaintext values are only decrypted in memory when a workflow runs or when
  you explicitly run `monoagentcli secret reveal <name> --reveal`

### Who can read a decrypted secret

The commands that print a decrypted value (`secret reveal --reveal`,
`secret export`, `connect get-oauth-client --reveal`, `profile info get
--reveal`, `node run vault.secret_get`, `workflow run --full-outputs`) and
`api key create` are refused (exit 3, `operator_only`) to a process the CLI
recognises as an agent, the in-app chat or a workflow run. By default no
secret resolves in a workflow you haven't approved, or in a step an agent
runs on its own (`node run`, MCP `node_run`); `secret agent-access on` lifts
these reference restrictions for the profile. The Reveal level additionally
permits plaintext retrieval with `secret reveal` and `vault.secret_get`, as
described under "Letting agent-written workflows read the vault" below.

This is a deterrent against mistakes and instructions planted in pages or
messages, not a barrier:

- An agent is recognised by markers in its environment. On Linux, markers
  in its parent processes count too, up to 32 levels; a deeper chain
  counts as marked, which also refuses your own terminal when it runs
  that deep (nested multiplexers or containers). A process whose marked parents have exited (`setsid`)
  is treated as you, and so is any process on macOS and Windows that clears
  its environment.
- The desktop app reveals by running the same commands as you. The CLI
  can't tell the app from an agent that runs them from an unmarked process.
- A workflow you approved resolves its secrets whoever runs it, and a value
  a step outputs under a field name the redaction doesn't know shows in its
  output. Only the references written in it resolve, though: one that the
  run's data or an expression produced (an agent's `--input`, a webhook
  body, a parent workflow) is refused, so the caller can't choose which
  secret a workflow reads.
- The HTTP API's `X-Full-Outputs: 1` gives whoever holds an API key
  unredacted outputs.
- A saved connection's token goes only to the host the connection names
  (its base URL). `http.request` and `http.graphql` send it to another
  address, or through a proxy, only when the address is written in a
  workflow you approved; an agent's `node run` and a workflow you haven't
  approved are refused (see "Where a saved connection's secret goes"
  below). A redirect to another host drops the credential and isn't
  followed when it would resend the request's body.
- `mcp.call` sends a vault server entry's headers only to the entry's own
  url, and doesn't follow a redirect to a loopback, private or link-local
  address.
- The extension commands that redirect the browser are yours: see
  "Extension commands that only the user runs" below.
- Writing secrets, connections and profiles stays open to an agent on
  purpose (`secret add|update|rm`, `connect save|set-oauth-client`,
  `profile create`): none of them shows a decrypted value. The exception is
  the HTTP API's own bearer, the vault entry `httpapi-token`, which only you
  write, since whoever sets it chooses what the API accepts. That one name
  is refused in the secrets package itself unless the write comes from the
  server or your own command, so a new writer can't reach it by accident.
- An agent with full access to your computer can do anything you can,
  including reading the keyring.

### Letting agent-written workflows read the vault

Each profile offers three levels through the Vault page and
`monoagentcli --profile <profile> secret agent-access [strict|use|reveal]`:

- **Strict** (default): no additional agent permission to use or retrieve vault secrets.
  Workflows explicitly approved by the operator retain their existing permissions.
- **Use without revealing** (`use --yes`): supported tools and workflows may
  resolve literal `@secret:` references. Direct plaintext retrieval stays refused.
- **Reveal** (`reveal --yes`): additionally permits `secret reveal --reveal` and
  `vault.secret_get` to retrieve plaintext. Internal authentication-token entries
  remain excluded. Plaintext reads recheck scope and expiry and write a name-only audit.

Existing grants become Use. `on` remains a compatibility alias for scoped Use,
with its existing required `--host` or `--any-host`; `off` aliases Strict.
The new `use` and `reveal` commands default to any host; Reveal cannot be
host-bound because plaintext leaves the controlled tool interface. Passwords
remain encrypted on disk at every level. Turning access off blocks subsequent
reads; values an agent already retrieved cannot be recalled.

- **Who sets it.** Only you: the command is refused (exit 3,
  `operator_only`) to an agent, the chat or a workflow, and so is clearing its
  audit. It lives in `profile_agent_secret_access`,
  outside any workflow definition, so no import or settings key can turn it on.
- **Default.** Off. A profile with no row behaves as before.
- **Scope.** `--secret NAME[,NAME...]` limits it to those vault entries (default
  all); `--expires 24h` ends it after that long (an expired grant is off);
  `--host HOST[,HOST...]` limits where a secret may be sent under Use. The
  legacy `on` alias requires `--host` or `--any-host`; `use` defaults to any host. The grant is read again at every
  resolution, so `agent-access off`, a narrower grant or an expiry stops
  a run already under way at its next read.
- **Where a secret may go.** With `--host`, a request of `http.request`,
  `http.graphql` or `mcp.call` (http), or the connection of a `trigger.sse`
  node, that carries a secret read under the switch (in its address, headers
  or body) is refused unless its host is on the list. A saved connection's
  token still goes only to the host the connection names, because the run
  stays unapproved (an agent's workflow sets no connection host of its own).
  With `--any-host` an agent's workflow can send an allowed secret to any
  address it names; use it only for a profile whose secrets you accept an
  agent using. That is the accepted risk of the broad mode.
- **Audit.** Each secret an unapproved run reads under the switch is
  recorded by name (workflow, run, step and time; never the value):
  `secret agent-access audit [--json]`; only you clear it (`--clear`). A read
  whose audit row cannot be written is refused.
- **Values in outputs under Use.** `vault.secret_get` stays refused to an unapproved
  run. Reveal explicitly permits its plaintext output. The values the run
  itself resolved
  (6 characters or more) are masked, wherever they occur, in the step inputs,
  outputs and errors it stores, so they don't show in `workflow_executions`,
  MCP or `--full-outputs`.
- **Masking is best-effort, and the switch trusts the workflow.** Masking
  matches values as text. It also covers the common forms a step derives from
  a value (base64 standard and URL, URL-encoded, JSON-escaped, upper and lower
  case, `-` and `_` swapped, hex), follows a sub-workflow the run calls, and
  survives a pause and resume. Any expression or code step can still transform
  a value into a form nobody listed (split it, reverse it, hash part of it),
  and a transformed value is not masked. The host check does not depend on
  recognising it: once any step of a run (or of the run that called it) has
  read a secret under `--host`, every request of `http.request`,
  `http.graphql`, `mcp.call` and `trigger.sse` in that run, proxy and
  redirect hops included, goes only to the listed hosts. A run that never read
  a secret sends anywhere, as before. The snapshot of a run paused while it
  holds such values (a wait or a review) is stored sealed under the profile's
  vault key, not as plain text.
  So `agent-access on` effectively trusts the agent-written workflows with
  the secrets and hosts you name: grant only secrets you accept an agent
  using, and prefer a short `--expires`.
- **Nodes under `--host`.** With `--host` (not `--any-host`) only the nodes
  that check their destination (`http.request`, `http.graphql`, `mcp.call`,
  `trigger.sse`) read a secret; any other node (mail, chat, database,
  file, shell, code and the rest) is refused the reference. A request that
  carries a secret may use a `proxy` only when its host is on the list, and
  never with `allow_unauthorized_certs`.
- **What still leaks.** A step can still move a value inside the run (an
  `http.request` whose response or another step's output is used as a field,
  a code step) or send it somewhere a host list does not look: the list
  covers the HTTP, GraphQL, MCP-http and SSE paths, nor does it look at a
  redirect that re-sends a request body. Values shorter than 6 characters are
  neither masked nor bound to hosts. Logs written to your terminal or files
  are not masked. A value that a service echoes back in a way that changes
  its text (encoded, split) is not masked.
- **What it does not lift.** The run stays unapproved: its file and program
  limits stay (`workflow_approvals`), a reference that came from run data or
  an expression is refused, and a failed approval check stays refused.
  Standalone steps use the same grant; `vault.secret_get` additionally requires Reveal.
  A person or process that can run `monoagentcli` outside an agent's markers
  can still turn the switch on, as they can approve a workflow: the bypasses
  are the same as for `workflow_approvals`.

### A copy of `~/.monoagent` and the key store

The key that opens the vault is in the OS key store, not in the database, and
the key store gives it to any program running as your OS user. So a copy of
`~/.monoagent/monoagent.db`, made by you, a script or an agent, gives any
process running as you every credential in it: API keys, OAuth tokens, website
logins and saved connections, in every profile. The CLI's own checks keep a copy
from using the two credentials that harm your account when they are used twice,
the monoes.me sign-in and the older library login (see "One sign-in per machine,
never copied" and "Network use"). They cannot stop a program that decrypts the
copy itself, or that reads the real database: such a program has what you have.

Since this release (#903) the vault key is also bound to the install it was
made in: the wrapped data key carries the canonical path of `monoagent.db`, so a
copy of the database in another place, from an agent, a script or a backup
restored elsewhere, does not open even though the key store gives it the same
key, and `doctor` reports `not-bound`. A database from an earlier release has
no binding: it is bound on the first open at your own `~/.monoagent` and refused
anywhere else. After a move you made on purpose (a renamed home directory, a
restore to a new place) run `monoagentcli secret rebind --from <old path of
monoagent.db>`; it is the user's own command (an agent is refused). This stops
the accidental copy only: a program that computes the original path's binding on
purpose, or reads the real database, still has what you have.

Two limits of the binding. The first open at your own install rewraps an
earlier release's key under the binding, and that is one-way: an older bundled
CLI (or app) opened on the same database afterwards sees a key mismatch, so
update the CLI, the app and the app-bundled CLI together. And the bound path is
compared as an exact string after symlinks are resolved: on a case-insensitive
filesystem (default macOS and Windows volumes) the same file reached with
different letter case is another path, so open the database by the exact path
it was bound to, or run `secret rebind`. The copy's error, `doctor` and
`secret rebind` name the path the key was made for when the database records it.

To keep a tool or an agent you do not trust away from your credentials, run it
where your key store is not: as another OS user, or in a container or virtual
machine that has neither your home directory nor your key store. A `HOME` of its
own is not enough.

### Agent mode: a sandbox and the scoped HTTP API

Against a process of your own OS user only an OS boundary works, so this is the
supported way to run a coding agent that you do not fully trust:

1. Print a sandbox profile and start the agent inside it. On Linux there are two bwrap profiles:
   `monoagentcli agent-mode sandbox --runtime bwrap --strict > agent-sandbox.sh`, then run
   `sh agent-sandbox.sh claude` from the project directory. **Strict is the supported separation.**
   The filesystem is read-only; `$HOME`, `/tmp`, `/var/tmp` and `/run` are empty in-memory mounts;
   only the current directory, the agent runtimes' own config (`~/.claude`, `~/.codex`,
   `~/.config/opencode`, `~/.gemini`, `~/.config/github-copilot`, which hold their logins) and paths
   you name with `--allow-rw` / `--allow-ro` are bound back. Whatever the agent writes elsewhere,
   shell startup files and autostart entries included, disappears with the sandbox, and the ssh/gpg
   agent sockets, the session bus, keyrings, `~/.ssh` and `~/.monoagent` are not there. The script
   refuses a working directory or allowed path that is your home directory, is under or contains
   a credential or startup location, or resolves into one through a symlink. The process also gets
   its own pid and IPC namespaces and session, no capabilities, and a cleared environment with
   an allowlist (`PATH`, `HOME`, the `MONOAGENT_API_*` variables and the runtimes' API-key and
   config-directory variables), so `SSH_AUTH_SOCK` and cloud credentials are not passed on.
   What stays writable outlives the sandbox: a git hook or editor task inside the current
   directory, and a hooks or settings change inside a runtime's config directory.
   `--compat` (the default for now, and what `--runtime bwrap` prints without `--strict`) keeps the whole
   filesystem writable and hides or protects a list of paths (state, keyrings, ssh/aws/gh/git
   credentials, the session bus, common startup and autostart files). It is best effort and reduces
   what a cooperative agent does by accident; it is not a boundary, because other places that start
   code later cannot all be listed and a writable parent can be renamed away. The macOS profile
   (`--runtime seatbelt`, `sandbox-exec`) denies reading and writing `~/.monoagent`,
   `~/Library/Keychains`, `~/.ssh`, `~/.aws`, `~/.config/gh`, `~/.git-credentials` and `~/.netrc`, denies
   looking up the keychain Mach services, and denies writing `~/Library/LaunchAgents` and the shell
   startup files; everything else stays as it is. It has not been run on a Mac by the authors.
   `--runtime claude-code` prints Claude Code's own sandbox settings.
2. The agent does not run `monoagentcli` as you. It calls the daemon's HTTP API
   (`127.0.0.1:9322`, `/profiles/{profile}/...`) with a token that opens that one
   profile only. The API is read-only unless the daemon was started with
   `--allow-mutations`.
3. Give the agent a scoped token, not the profile's own bearer. You make it, outside the
   sandbox, as yourself (creating and revoking a token, and reading one out with `env`, are
   refused to an agent, the chat and a workflow, and the API has no route that makes one):

   ```sh
   monoagentcli --profile work agent-mode token create --name reviewer --scope read --expires 24h
   eval "$(monoagentcli --profile work agent-mode env --token reviewer)"   # MONOAGENT_API_URL, MONOAGENT_API_TOKEN
   monoagentcli agent-mode sandbox --runtime bwrap > agent-sandbox.sh
   sh agent-sandbox.sh claude      # the variables pass through into the sandbox
   ```

   Scopes (each includes the ones before it): `read` is GET only (list, get, status);
   `write` also validate and the changes that start and send nothing (today: tagging and
   untagging a document); `run` also runs, activates and
   deactivates workflows. Mind what `run` means: it is the authority of every approved or active
   workflow of the profile, including ones that send mail or read the vault, so scope a token to
   what the agent should be able to start. The engine's approval gates apply whatever the caller:
   an unapproved or AI-authored workflow is still refused `@secret` references and
   `vault.secret_get`. Approvals (`hil approve|reject`) and anything else that spends the
   profile's authority need the full scope, which only the profile's own bearer has; `X-Full-Outputs`
   (unredacted run outputs) is honoured for that bearer alone. A request above the token's scope is
   answered 403. A token
   expires (24h unless `--expires` says otherwise, `never` to opt out), is revoked at once with
   `agent-mode token revoke`, and only its SHA-256 is kept for checking it (compared in constant
   time); its text is kept in the profile's vault so that `env` can hand it out, and removed on
   revoke. Every request made with a token is recorded by token name, method, escaped path
   (cut to 200 characters) and status, never the token: `agent-mode token audit <name>`. A valid token used on another profile's path is refused 401 and recorded too. The row
   is written before the request runs, and a token whose use cannot be recorded is refused with
   503; the table is trimmed by count, never by age or id: a token keeps its last 2,000 served rows and, in a set
   served calls cannot push out, its last 500 refused ones (401, 403), and over 50,000 rows in all the token holding the most loses its oldest first, so one token's traffic cannot erase another's trail. Vault entries named `agent-token-*` hold the tokens'
   text: only the token commands write them, a workflow run never reads them (even with agent
   secret access on), and an expired token's entry is removed the next time the tokens are listed. A token opens the profile it was made for and
   no other. The profile's own bearer (`secret reveal httpapi-token --reveal`) keeps working as the
   full scope; start the server with `MONOAGENT_HTTPAPI_DISABLE_PROFILE_BEARER=1` to refuse it, so
   that only scoped tokens open the API. Do not let the agent read the vault (see the agent secret
   access setting) or it could read a wider token's text.
4. `monoagentcli agent-mode status` (and an informational line in `doctor`) says whether the
   process looks sandboxed: the `MONOAGENT_SANDBOX` marker the bwrap profile sets, a `~/.monoagent`
   that cannot be listed, is empty or is a tmpfs mount, macOS App Sandbox's container id. This is
   advisory only: a process can hide the marks, and finding none proves nothing. The sandbox is the
   boundary, never this report.

The API server answers 421 to any request whose `Host` header is not a loopback spelling of
the port it is bound to (`127.0.0.1`, `localhost`, `[::1]`), before routing and before any token
check, so a web page cannot reach it under another name. A server bound to a non-loopback address
serves other names (a LAN IP, a hostname) only when you list them in `MONOAGENT_HTTPAPI_ALLOWED_HOSTS=host:port,...`; without it that server prints a warning at start and answers those requests with 421. This check covers the control API; the webhook listener and the OpenAI-compatible `/v1` listener can be reached by any Host and rely on their own authentication.

Limits: the operator check (the markers and process-ancestry test that decide whether a command
is the user's own terminal) is a convenience guard, not a security boundary; an OS sandbox as above is
the supported way to keep an agent apart from your keys. Windows has no OS boundary from this tool (see
below). The profiles are written for Linux (bwrap), macOS (seatbelt) and Claude
Code's settings; the seatbelt service names and Claude Code's setting keys were
not checked against every release, so test that the agent cannot read
`~/.monoagent` before you rely on one. An abstract-socket D-Bus session bus (some
distributions) is not hidden by file masking. A token is the agent's
credential: anyone holding it can use the API as that profile within its scope. A
profile's own bearer also allows the `X-Full-Outputs` header.
Windows has no profile: it has no sandbox that a command can start a process in without
extra installs, so `agent-mode sandbox --runtime windows` refuses and says what to use instead:
WSL2 with the bwrap profile, a separate Windows user account for the agent, or Windows Sandbox / a VM,
each reaching the API with a scoped token. The steps, and what they do not protect against, are in
`monoagentcli agent-mode windows` (the file `docs/agent-mode-windows.md`, embedded in the binary); `monoagentcli doctor --agent-account NAME` checks that the
account has no access to the state folder.

What a token reaches besides workflows (all under `/profiles/{profile}/`, the token's own profile
only; another profile's id is "not found", and a token used on another profile's path is refused
401 and recorded in its audit):

- `read`: `GET documents` (`?limit=` up to 200, `?offset=`, `?tag=` up to 5), `documents/{id}`,
  `documents/{id}/tags`; `GET people` (`?platform=`, `?search=`, `?limit=`) and `people/{id}`, the
  fields `person_list` and `person_get` return, so a person's contact details and notes are readable
  (each text is cut at 4,096 characters and a person's answer at about 64 K characters); `GET tasks`
  (`?status=`, `?limit=`), `tasks/{id}` (the latest 100 events) and `tasks/next`, which looks and
  claims nothing. The board is read as an agent: the Inbox is hidden unless you let agents see it,
  and task texts come back in `_untrusted` fields.
- `write`, only on a daemon started with `--allow-mutations` (otherwise 404): `POST
  documents/{id}/tags` and `DELETE documents/{id}/tags/{tag}`. A tag name is at most 64 characters, a
  document holds 10 tags (the CLI's rule) and the route makes no new tag once a profile has 500
  (counted and written in one transaction). The API cannot delete a tag, only unlink it, so tags a
  write token made stay until you delete them; and a write token can untag any tag on any document
  of its profile (the audit keeps the path, not the body). Tag names refuse control and format
  characters (bidi overrides, zero-width characters).

A document is shown without its file path, capture directory or index error (which can quote
a path), and a captured page's address without its query and fragment; the path part stays, so a
secret embedded in a path (`/reset/<token>`) of a captured URL is returned. Documents and people
come back like tasks do: a `note` and every free-text field under a name ending in `_untrusted`.
The Inbox is not readable by a token (`?status=inbox`, an Inbox task by number: nothing, 404)
unless you turned on the agents' board view. There is no route that
reads a document's content: the agent gets the metadata, not the file, and the file stays behind
the sandbox. Every list is paged (default 50, at most 200), unknown query parameters are refused
400, ids are checked against `[A-Za-z0-9][A-Za-z0-9._:-]*`, a tag body over 1 KiB is 413. Still out
of reach of every token, by there being no route: creating, editing, moving, claiming, finishing or
deleting a task; adding, editing or deleting a person or a document; reading a document's content;
anything that sends or publishes; the vault, secrets and connections; profile folders and settings;
and token management. A person's or task's text is whatever was captured or written, so treat it as
untrusted input to the agent (prompt injection) and as personal data you are handing to it: give a
`read` token to an agent you would let read the profile's CRM and board.

Still deferred: unlocking the vault only with the user present, the macOS keychain ACL (needs a
signed binary), hiding an abstract-socket D-Bus session bus under `--unshare-net`, a token that
reads less than `read` (a documents-only or no-people token), and write routes for tasks, people and
document content (they need the approval cards the CLI shows).

### Where a saved connection's secret goes

A step that uses a saved connection (its `credential_id`, or the connection
`node run` and MCP `node_run` find for the node's platform) sends the
connection's password, token or key to the host its settings name.

In releases up to and including v0.152.0, a step's own settings won over
the connection's. So a coding agent on your computer, the in-app chat or a
workflow you hadn't approved could send a saved connection's password or
token to a server it chose. Examples:

- `comm.email_read` with its own `imap_host`, with no approval card.
- `service.mastodon` reading with its own `instance`, with no card.
- `comm.email_send` with its own `smtp_host`, when you had always allowed
  the recipient or turned send approvals off. The mail and your mail
  password went to that host. With a card, approving sent through your
  own server.

An app node's region setting (S3) could move the AWS access key id and
session token to another host. An app node's redirect to another host
kept the connection's API key headers, and a 307 or 308 sent a body
holding the key there again.

Most such steps aren't sends, so no card held them. Default installs were
affected. MCP's `node_run` was affected only if you registered monoagent's
MCP server with `--allow-mutations`. We know of no use of this: it was
found by code review and confirmed by tests with fake servers. If you're
concerned, rotate the passwords and API keys of the connections an agent
could reach. `monoagentcli connect list --profile <id>` lists them.

Now the settings that decide where the secret goes follow these rules:

- **The connection names the host.** The step can't name another one. A
  different SMTP or IMAP host or port, Mastodon instance, Jira, Salesforce
  or Shopify site, FTP, SSH or LDAP server, Kafka, RabbitMQ, AMQP or MQTT
  broker, Snowflake account, or embedding or vector store URL is refused.
  The error names the field and the connection's host. To use another
  host, change the connection.
- **The connection names no host.** The node's own default is used.
  A host the step sets must be written in a workflow you approved. A host
  taken from the run's data (its input, a webhook body, an earlier step,
  an AI tool's arguments) is refused. So is a host in a workflow you
  haven't approved, or in a step an agent runs on its own.
- **`http.request` and `http.graphql` stay open, by your decision.** Their
  URL, endpoint and proxy may name any address, in any run, with a saved
  connection. A next page on another host, which the response names, now
  goes without the request's auth, as a redirect does.
- **API packages (`app.*` nodes).** They send the credential, and
  anything rendered from the connection, only to the hosts the package or
  the connection names. That covers the base URL's host as the package and
  connection render it, and its subdomains. It also covers the
  connection's own server, hosts written in the package, and its
  `auth_hosts` (such as S3's regions). A URL or a host part, such as an S3
  region, that a parameter or a response points at any other host goes
  without them. A redirect to another host, or from https to plain http,
  carries none of the headers the connection set. One that would send a
  body there again (307, 308) isn't followed.
- **Model vendors.** `agent.ask` and `ai.model` reach the vendor the
  connection is for. An embedding provider's region or location must be a
  region name.
- **Queue triggers.** Kafka, RabbitMQ, AMQP and MQTT triggers check the
  same rules when they start. Brokers the connection doesn't name come
  only from a workflow you approved.
- **The send approval card.** In the app and in `hil show`, the card names
  the mail relay or Mastodon instance a send goes through, and the TwiML
  URL a Twilio call fetches. That URL must be a public http(s) address,
  not this machine or a private network.

What stays open:

- A workflow you approved may name a host for a connection that names
  none, and the secret goes there whoever runs the workflow.

### File-based keyring fallback (weaker posture)

When `MONOAGENT_ALLOW_FILE_KEYRING=1` is set and no OS keyring is
available, the key-encryption key (KEK) is stored in a per-profile file at
`~/.monoagent/vault/.file-keyring-<profileID>` (permissions 0600) instead
of the OS keyring, and the CLI prints a warning whenever it uses it. This
is deliberately opt-in and still weaker than a real OS keychain's
process-scoped access control — but the KEK is no longer stored raw.

The file holds the KEK **wrapped** with AES-256-GCM under a key derived
from an operator-supplied passphrase via argon2id (same tuning as the
vault export/import format — see `internal/secrets/export.go`), in a JSON
envelope alongside its salt and nonce (`internal/secrets/filekeyring.go`).
The passphrase is read from stdin/an interactive prompt (or a chmod-600
file named by `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE`, see below), never
accepted as a CLI flag or environment variable — the same anti-argv
pattern `secret add` and vault export/import already use, since both flags
and env vars leak through shell history and process listings. Without the
correct passphrase, the file alone (e.g. leaked via a backup or a
misconfigured volume) is no longer sufficient to decrypt the vault —
unlike the pre-hardening raw-KEK format. Payloads remain AES-256-GCM
encrypted either way; the passphrase adds a second factor specifically for
the KEK-at-rest.

**Migration from the old raw-KEK format:** a vault created before this
hardening has its file-based KEK stored as 32 raw bytes with no wrapping.
The first read after upgrading detects this (the file isn't valid JSON but
is exactly 32 bytes), prints a migration warning, prompts once for a new
passphrase, and re-wraps the *same* KEK bytes into the new envelope format
in place — existing `vault_keys` rows (and therefore every already-stored
secret) keep decrypting correctly, since only the KEK's on-disk
representation changes, not its value. Set the new passphrase when
prompted; there is no separate manual migration step required.

Use the fallback only where no keyring exists (headless CI, containers);
without the `MONOAGENT_ALLOW_FILE_KEYRING` variable set, secret operations
fail closed.

**Headless/CI use:** pipe the passphrase via stdin from a masked CI
secret — never as an argument or plain environment variable:

```bash
echo "$VAULT_PASSPHRASE" | monoagentcli secret reveal my-key --reveal
```

Each CLI invocation that actually needs the KEK (decrypting a secret via
`secret reveal`/`secret get`, adding one via `secret add`, or a `workflow
run`/`daemon` process resolving `@secret:` refs) prompts for the
passphrase on stdin the first time that process touches it; further
operations *within that same process* — e.g. every `@secret:` resolution
during one `daemon` run — reuse the in-memory KEK without prompting again
(the OS-keychain path memoizes identically — see
`internal/secrets/keyring.go`'s `getOrCreateKEK`). Separate CLI
invocations are separate processes, so each one prompts once. Store
`VAULT_PASSPHRASE` as a masked/protected CI secret and pipe it into each
invocation that needs it.

Commands that *also* read their own input from stdin (`secret add` reading
the value, `secret add|update --stdin-json`, `secret import`'s export
passphrase, `jev key set`, `node run --stdin`, `jev --request -`, the
`mcp` server) never read the KEK passphrase from stdin: they prompt on the
controlling terminal (`/dev/tty`) instead. Where there is no terminal
(the desktop app, a service), store the passphrase in a file only you can
read and point `MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE` at it:

```bash
install -m 600 /dev/null ~/.config/monoagent-keyring-pass
$EDITOR ~/.config/monoagent-keyring-pass   # passphrase on the first line
export MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE=~/.config/monoagent-keyring-pass
```

The variable holds a path, never the passphrase; a file readable by group
or others is refused. When it is set it wins over stdin and the terminal.
Without it, a command that has no source fails with an error naming it.

Without an environment variable, the same can be configured once with
`monoagentcli secret keyring set-passphrase` (passphrase on stdin, or typed
at a no-echo prompt) — or from the desktop app's **Settings › Vault
keyring**, shown only on hosts with no OS keychain. It writes
`~/.monoagent/keyring-passphrase` (mode 0600, `~/.monoagent` 0700), after
checking the passphrase unlocks every existing file keyring; a passphrase
that doesn't is rejected and nothing is written. `secret keyring status`
reports the key store (`os`/`file`/`unavailable`) and the passphrase
file's state without prompting; `secret keyring clear-passphrase` deletes
it. The passphrase is looked up in this order: the file named by
`MONOAGENT_FILE_KEYRING_PASSPHRASE_FILE`, `~/.monoagent/keyring-passphrase`,
stdin, the terminal. Keeping the passphrase next to the keyring means
anyone who can read your home directory as you can open the vault — the
same trade-off as the environment-variable file.

## An agent's sends wait for you

When a coding agent on your computer, the in-app chat or a workflow you
haven't approved tries to send a message or post (email, Outlook, Gmail,
Slack, Telegram, Twilio, WhatsApp, Discord, Reddit, Mastodon, Bluesky,
dev.to, Hashnode, Product Hunt and YouTube, the social browser actions that
message, comment, post or follow, and the API packages' sends), nothing goes
out. The message becomes a card in Human in Loop and is sent only when you
approve it. Coder mode's `sendApproval` setting turns this off. An agent is
recognised as described in
[Who can read a decrypted secret](#who-can-read-a-decrypted-secret), with the
same limits.

What isn't held: likes, reposts, boosts and reactions (they carry none of your
text); other changes on outside services (an issue, a record, a payment, a
file made public), which a grant's outbound check covers for an org role
([Org automations and autonomy](#org-automations-and-autonomy)); and general
steps that can reach any address (`http.request`, `core.code`'s JavaScript).

### Sends that skipped approval up to v0.152.0

In releases up to and including v0.152.0, an agent, the in-app chat or a
workflow you hadn't approved could send some messages without a card:

- with `node run` under the short names `telegram`, `gmail`, `discord`,
  `slack`, `twilio`, `whatsapp` or `email_send`, when the profile it named had
  a saved connection for that service;
- in a workflow, with the old node names: a Mastodon post (`comm.mastodon`), a
  Reddit reply (`comm.reddit`), or a Discord or Outlook send with no operation
  set (`comm.discord`, `comm.outlook_send`);
- with an operation written as a number or true/false on Telegram, Discord or
  Outlook, which send a message when no operation is named;
- through a messaging step attached to an AI agent step as a tool, whenever
  the model called it, in any workflow: tool calls weren't checked at all;
- with `people messages compose`, `reply` and `send-draft`, which sent email
  through the profile's saved Outlook connection.

Default installs were affected. MCP's `node_run` was affected only if you
registered monoagent's MCP server with `--allow-mutations` (setup asks, and
the default is no). If you're worried, compare what was sent from your
connected accounts with the approvals you remember: each service's own sent
history, and in the app the Communications tab, the messages on people's pages
and the run history. A send that skipped approval left no card, so the
service's own history is the full record. We know of no use of this: it was
found by code review and confirmed by tests with fake senders. A workflow you
approved and run yourself sends as before, its AI agent's tools included.

Technical detail: the gate looked a node up by the name it was given rather
than the node type that runs it, and read an operation that is no string
differently from the node. It now resolves every name to that type and reads
the operation through the same helper as the nodes, and a tool call and the
`people messages` sends pass it as a step does. A test walks every `comm.*`
and `service.*` node type and every alias, through both `node run` and a
workflow run, so a new name or operation can't slip past. Operations that are
no string are tested against Telegram's, Discord's and Outlook's own code, and
in the walk for every send type.

### Recording replays and saved recordings up to v0.152.0

In releases up to and including v0.152.0, an agent could replay a recording,
its own or yours, with `record replay`, or run a draft's steps with `record
verify`, in your logged-in browser, with no approval: which steps count as
changing a site is read from the recording or draft itself, a file an agent
can write. It could also save a draft with `record save` and run it with
`node run`, MCP `node_run` or a workflow step, and so with a browser package
it wrote by hand under `~/.monoagent/actions`. Now:

- `record replay`, `record verify` and `record save` are yours: an agent, the
  chat or a workflow gets exit 3 (`operator_only`). Only `record replay
  --dry-run`, which runs nothing, stays open.
- A browser action of a package that isn't built-in (a saved recording, a
  local package, an imported one) doesn't run for an agent, the chat or a
  workflow you haven't approved, through `node run`, MCP `node_run` or a
  workflow step: it is refused before a tab opens. Built-in packages run as
  before, and their sends wait for you (above). A symlinked `actions` folder
  loads as a local package and is refused the same way; a symlinked package
  folder inside it isn't loaded.
- `login <package>` and `login confirm <package>` of a package that isn't
  built-in are yours too: such a package names its own login page and the
  sites whose cookies its login keeps.
- `automation rerecord` is yours: it opens a package's site, or any `--url`,
  in your browser and rewrites the selector your click picks, which changes
  what the package's actions click, a built-in one's too. So are `automation
  doctor accept-repair`, which rewrites one without a click, and changing a
  package's flags with `automation trust` (page scripts, real runs);
  showing them stays open. `login names` refuses an agent when it would
  open the site of a package that isn't built-in.
- A package counts as built-in only while its files hash to an official
  release of it. The hashes of the official packages (the ones in
  `automations/`) ship inside the binary, and every run, `automation list`
  and `automation info` checks the installed files against them. An agent
  that edits a built-in package's files, or files its own package as
  built-in in `~/.monoagent/automations`, gets an imported package, which
  these checks refuse it. Still open: the overlay of selector fixes beside a
  built-in is not hashed, and an official library item that is not in
  `automations/` has no shipped hash and installs as imported. Hashes signed
  by monoes.me would cover both (#900).
  The check also requires the tree to hold only regular files and
  directories (a symlink, special file or dotfile makes the package
  imported), runs a built-in from the files that were hashed, and files each
  hash for one release version with a minimum trusted version per package
  (`official_hashes.json`): an older official release below that minimum is
  imported, not built-in, so a known-vulnerable release cannot be put back
  under the built-in tier. An official item newer than the app's shipped
  hashes installs as imported, and `library install`/`update` warns that
  agents are refused it until the app is updated.
- If you start the app, the daemon or the browser bridge from an agent's
  shell, a chat or a workflow run, it inherits that agent's markers, and
  your own replays, verifies, logins and runs in it are refused as an
  agent's. Restart it from your own terminal or the desktop launcher; the
  refusal says so.
- The browser extension's verify reaches the app through the bridge, which
  any program that can read its pairing file can use, so the bridge's verify
  takes no `full`, opens a blank tab and stops before the first step that
  touches a page, whatever the draft marks. It checks much less than before.
- Your own replays, verifies and runs, in your terminal and the app, and
  the workflows you approved, work as before.
- An agent that hides its markers and owns a terminal can still answer a
  replay's question and pass as you: the general limit of how agents are
  recognised (see "Who can read a decrypted secret").

We know of no use of this: it was found by code review.

## Assistant tools (chat `--tools`)

The `chat` command can expose a monoagent tool surface to the assistant
model. Guardrails:

- Tools are **off by default**; enabling them is explicit (CLI
  `--tools monoagent`; run/execution tools additionally require
  `--tools monoagent,runs`). The GUI settings toggle mirrors the gate
  and also defaults to off.
- Vault tooling returns entry **metadata only** — secret values are
  never returned to the model.
- Workflow definitions returned by `get_workflow` are redacted for
  credential-shaped values.
- Destructive (delete-class) tools write a sidecar backup of the
  affected record before deleting.
- Message content synced from connected mail accounts is wrapped in
  provenance fences so the model can attribute it.
- Tool-call timeouts derive from the caller's context, so cancelled
  sessions stop in-flight tool work.

**Residual risk, stated honestly:** prompt injection cannot be fully
eliminated when assistant context includes synced message content — a
crafted message can attempt to steer the model toward whatever tools are
enabled. The gates above bound the blast radius, not the attempt. The
recommendation is to keep tools off (the default) when chatting over
mail synced from sources you do not trust, and to enable `runs` only in
trusted sessions.

### The extension's side-panel chat (`--tools monoagent:read`)

The MonoAgent Bridge extension can chat about the page you are looking at.
That page is text written by whoever controls it, so every turn the
extension starts runs in a mode of its own, `--tools monoagent:read`
(with or without a page attached; one mode, so a later message cannot
widen what an earlier page reached the model with):

- **Six tools only**: `list_workflows`, `get_workflow`,
  `list_node_types`, `propose_task`, `propose_recording` and
  `propose_vault_save`. The three proposals write nothing. `propose_task` answers with the task it
  proposes, the side panel shows it as a card (marked "Suggested after
  reading a web page" once the conversation has read a page), and only the
  person's click on the card's Add button sends it to MonoAgent, which
  files it in the Inbox as the agent `agent:extension-chat`, never in
  Ready. `propose_recording` answers with a goal and the steps for the
  person to perform; its card (marked the same way) starts the side
  panel's recorder only when the person presses Start recording, and the
  automation is saved only by the Record section's own Save, after the
  person has reviewed the draft. No tool starts a recording, and what is
  recorded never goes back to the chat. `propose_vault_save` names one
  item, an address on the page or the person's selection, and carries no
  bytes; only the person's click on Save to vault makes the side panel
  check that the item is on the page they are on now (or read their
  selection at that moment), fetch it within a 20 MiB cap (MonoAgent's
  own), and send it to MonoAgent's file vault as `vault.save`. The fetch
  carries the person's cookies only when the file is on the page's own
  origin, so a page cannot have a request to another site go out with
  them. The card names the host the file would come from. A public page
  never has the extension fetch a private network address: loopback,
  private, carrier-grade NAT, link-local, benchmarking, multicast and
  reserved IPv4 ranges; IPv6 loopback, unique-local, link- and site-local
  and multicast addresses, and those that carry an IPv4 address (mapped,
  NAT64, 6to4); `localhost` and the home, office and tailnet names (`.lan`,
  `.home`, `.home.arpa`, `.corp`, `.local`, `.localdomain`, `.internal`,
  `.intranet`, `.private`, `.ts.net`, single-label names). A page counts
  as private, and may save such an address, only when it is an http or
  https page on a private IP address, `localhost` or `*.localhost`; a page
  on any name, including the network names above and the wildcard-DNS
  services below (which can be served from anywhere), a file, a blob,
  `about:`, `data:` and the browser's own pages count as public.
  Two limits are known. A public name that resolves to a private address
  through DNS is not caught: the extension cannot see the address a name
  resolves to, only the name; the known wildcard-DNS services (`nip.io`,
  `sslip.io`, `xip.io`, `xip.name`, `1u.ms`, `localtest.me`, `lvh.me`,
  `traefik.me`, `vcap.me`, `lacolhost.com`, `fbi.com`, `localho.st`,
  `local.gd`) are refused as private. And from a public page an address that redirects is
  refused rather than followed, even when the redirect is harmless, so
  some CDN links need "open the file itself, then save it from there". A
  page cannot press those buttons:
  they are in the side panel, and the worker takes the requests only from
  the extension's own pages. There is no secret, credential, secret-vault, profile
  document, synced message, contact, publication, org, template or social
  list tool; no tool that writes or deletes anything; no `run_workflow`;
  no workflow or canvas builder; no Bash; no knowledge-graph search. A tool
  that is not in that list is not registered with the model, and a call
  made for it anyway is refused ("not available in this session").
- **Frames and page actions.** When the page's own text is thin, the panel
  also reads every frame. Text from a frame of another origin is capped
  (2000 characters each) and fenced as `[frame origin: <origin> - untrusted
  frame content]` and closed by `[/frame content]` (the marker and header
  patterns are stripped from the frame's own text), but it is still
  page-controlled text that reaches the model, so it is a prompt-injection surface like the rest of the page.
  What it can lead to is bounded as for any page text: a suggested step is
  only a card, and nothing happens until the person presses Run. At Run the
  step is found by its words across the tab's frames and done in the one
  frame that matches; a step in an iframe is refused if that frame's
  address changed since it was searched, and the finished card names the
  frame's origin. Typing, choosing or ticking in a frame of another origin
  is refused until the card has shown that frame's origin and the person
  presses Run a second time. The click guard looks through shadow roots
  for a form, applies the verb list (including accept, agree, approve,
  continue, proceed) to every button, link, tabindex, role or `onclick`
  target, blocks `javascript:` and `data:` links, and reads a link's path
  and query for those verbs. Ticking a box or choosing a drop-down option is held
  back (not done; the person does it) when the label or option says
  something that grants, accepts, pays or deletes, and a box is only ticked
  when the page ties it to the named text (label, `for`, `aria-labelledby`,
  or its own name).
  The guard reads words after folding: full-width and other compatibility
  forms, zero-width, soft-hyphen and bidi characters, combining marks and
  case do not hide them. It also holds `payment`, `disconnect`, `unlink`,
  and a text of any length with such a verb that has a pointer cursor, a
  `tabindex` or sits in a form. Remaining limit: a box with a default cursor,
  outside a form, whose click listener acts on a long text cannot be told
  from plain text and is not held; look-alike letters from another script
  (a Cyrillic `а` in `delete`) are not folded either.
- The page counts as untrusted content for every gate that remains
  (`checkInjectionGate`), and a model-supplied `confirm:true` is never
  consent for a gated tool: the gate is checked before `confirm` is read.
- The page is delivered inside a fence the page cannot close (no bracket
  characters survive in page fields; zero-width and control characters are
  dropped; url and title are one line each; the page text comes last, and a
  line after the fence says it has ended, and a fixed last line,
  `[the person's message follows]`, which no page field can spell, marks
  where the person's own words begin; the panel uses it to show only what
  was typed). The address keeps scheme, host, path and query, with the same
  rules in the extension and again in MonoAgent: credentials are dropped; a
  query (parameters separated by `&` or `;`; names are decoded up to three
  times) value is kept only when it is not opaque (20 or more base64, hex or
  url-safe characters, except a playlist `list`) and either its name is one of
  `v list index t q query search_query p page id tab sort lang hl` and the
  value is at most 64 characters, or the value is at most 16 characters and
  the name does not contain (any case) `token code key
  secret pass pwd auth session sid sig signature credential otp nonce state
  csrf reset verify magic ticket jwt bearer hmac expires x-amz x-goog
  access refresh id_token api apikey`; every other value becomes
  `REDACTED`; a path segment of 20 or more url-safe characters with a digit,
  a letter and at most one `-` or `_` becomes `REDACTED`; the fragment is
  kept only as a plain anchor (letters, digits, `.`, `_`, `-`, at most 64)
  that does not look like a token (starts with `eyJ`, or three dotted parts
  of 8 or more characters), so OAuth fragments are dropped; the whole
  address is read and redacted before it is cut to 1 KiB (an address over
  32 KiB loses its query and fragment). Every line of page-written text
  inside the fence (text, selection, description, transcript) starts with
  `| `, so a line the page forges cannot look like a field of the fence.
  Residual risk: a secret in a parameter with an
  innocent name, a short value (16 characters or fewer) or an allowlisted
  name can still reach the model; turn page sharing off for such a page.
  On a YouTube video page the captions, channel and description are read
  from the person's own tab (the same reader as "Save video summary") and
  sent inside the fence as untrusted data, only when page sharing is on.
  The text of a `file://` page is never sent; caps are in UTF-8 bytes and
  cut at a character boundary.
- The message, page text included, reaches the chat process on stdin
  (`--prompt-stdin`), not on its command line.
- Replies show explicit Markdown links only, each with its real host beside
  it; bare addresses stay text. The first message of a conversation shares
  the page by default, later ones do not unless you turn the chip on, and a
  send is refused if the active tab is no longer the one the chip showed.

**Residual risk:** hidden text on a page can still steer what the three
read tools show (workflow definitions, which may name accounts or targets)
and the wording of the model's reply, including a link you may choose to
click (its host is shown), and the task it proposes: read a card before you
add it, as you read an Inbox task before approving it. The page text and the reply stay in the local
conversation database like any chat.

**Pictures in the chat (screenshots and attached images).** A screenshot of
the tab, or pictures the person attaches, can go to the model, each only by
the person's choice. A screenshot can show what the page text skips, so a
look card shows it to the person first and waits for "Send to the model";
that agreement is kept per site (origin) within one conversation, never for
the conversation as a whole, so a page that earns one agreement cannot have a
later card capture another site on a single Run. A capture is refused for a
page that is not http(s), a literal private-network address, a tab that is
not showing, and a tab whose id, full address, window or visibility changes
between just before and just after the picture. The bridge and the CLI both
re-check every image from its own bytes: PNG, JPEG, GIF or WebP only (not
SVG), at most 3 of 2 MiB, at most 8192 pixels a side and 25 megapixels as the
header claims (a few bytes can claim hundreds of megabytes), no animated GIF
or WebP, and a header that cannot be read is refused; attached files over
20 MiB are refused before the browser decodes them. The files are written
0600 in a private folder for the one turn and removed after it; the bridge
removes `monoagent-chat-images-*` folders of this user older than an hour at
start, in case a crash or kill skipped that. Limit: the private-network check
looks at the literal host only, so an internal hostname that resolves to a
private address is not caught; the consent step is the control there.

### Saving to the vault from the extension (`vault.save`)

The side panel's confirm card saves an image or document you clicked Save on
into the image/file vault of the profile the panel is set to, the vault
`image list` and the MCP tools `vault_item_list` and `vault_item_get_path`
read. It is one bridge request method, `vault.save`
(`internal/extension/vault_save.go`):

- **Who can call it.** Only a paired extension socket: the bridge reads no
  request frame until the socket has passed the token handshake, and a
  request that reached the handler without a connection is refused. The
  bridge cannot tell a click from a script, so the click is the extension's
  half: it sends `vault.save` only from the confirm card's Save handler, and
  no chat tool, agent or page script in the extension reaches that call.
  **Token caveat (as for every bridge method, #858):** anything running as
  your user can read `~/.monoagent/extension.token`, pair a socket of its own
  and call `vault.save` with no click. The bridge is a courtesy layer against
  mistakes and page-borne instructions, not a sandbox against your own
  processes.
- **It writes and never reads.** The reply names the item it made, or for a
  client id already saved the item as stored (`id`, `created`, `name`,
  `mime`, `size_bytes`); it never carries the file, its path or anything else
  from the vault, and the bridge has no other `vault.*` method.
- **Checked before anything is written:** at most 20 MiB once decoded (a
  request is one WebSocket frame, read up to 32 MiB); only PNG, JPEG, GIF,
  WebP, PDF, plain text, Markdown and CSV, and the bytes must sniff as the
  type they claim (no SVG, HTML or executables); the name is a label of at
  most 255 bytes with no path separator, control or bidi character and not
  `.` or `..` (the file itself is stored as `img-NNN` plus the type's
  extension, 0600, never under the sent name); the profile id is a valid
  folder name and must be a profile that exists now, so a profile deleted
  since the panel listed it gets no folder back; the source address is kept
  only as http(s) scheme, host and path (no user-info, query, fragment or a
  segment's `;parameters` such as `;jsessionid=`, so a presigned URL's
  signature never shows in `image list` or the app). A token that is itself
  a path segment is kept: nothing tells it from a file name.
- **Limits per profile.** Files saved this way (vault items with a client id)
  may total at most **2 GiB** and **5,000 items** per profile; the count and
  the sum are read inside the save's own `BEGIN IMMEDIATE` transaction, so
  saves at once cannot pass them together. Past either, the save is refused
  with `quota_exceeded`, naming the limit (final: delete saved files in the
  Image Vault). At most **20 saves a minute per profile** and **60 a minute in
  all**, counted in memory in the bridge process; past either, `rate_limited`
  with `retry_after` in seconds. A save is counted only once its profile is
  known to exist, from a list of live profiles read at most once a second, so
  made-up profile ids get neither a rate window nor any database work; the
  file is decoded only after the rate check.
  Accepted trade-off: the 60 a minute are shared, so a paired extension that
  keeps sending junk saves on three or more live profiles can hold real saves
  back for up to a minute. That takes the same pairing that can already write
  60 files a minute, and the app and the CLI do not go through this limiter.
- **Exactly one write per save.** Each request carries a client id; the
  vault keeps one item per client id per profile (a unique index), so a retry
  returns the item it already made and writes nothing. A file or symlink
  already in the vault folder under the next item's name (left by a crash, or
  put there by anyone) is never overwritten or followed: the save takes the
  next free number, at most 5 times, then is refused with `storage_conflict`
  (final: the message says to check the profile's vault folder).
- **Documents stay out of the knowledge base.** A PDF, text, Markdown or CSV
  file goes into the image/file vault too, not the documents vault
  (`profile documents`): documents there are indexed into the knowledge base
  that agents read, and content from a web page must not land in it without
  the person adding it there themselves. For the same reason a save is not
  indexed in the knowledge graph; it is recorded with source `extension`.

### Extension commands that only the user runs

`extension bind`, `extension unbind`, `extension reset`, `extension pair
--open` and `extension install --zip` refuse an agent, the chat and a
workflow run: any process carrying one of the operator markers, or whose
parent process carries one. They exit 3 with code `operator_only`.

- `bind` and `unbind` decide which profile a browser acts as, so an agent
  could point your browser, with its logged-in accounts, at another
  profile, or take its profile away.
- `reset` revokes the pairing token and cuts every browser off until you
  pair it again.
- `pair --open` opens a pairing page that pairs the browser with whichever
  bridge served it, so an agent running a bridge of its own could take
  your browser over.
- `install --zip` unpacks a zip that no release signed into the folder the
  browser loads the extension from.

Printing the token (`extension pair` without `--open`) stays open, because
anything running as you can read `~/.monoagent/extension.token`. So does
the plain `extension install`, which unpacks only the extension built into
the CLI (the binary itself is verified by the signed update path). `serve`, `status` and
`browsers` stay open too: they change no binding, rotate no token, and
agents use `serve` for private test bridges. Like the other operator
checks, this keeps agents from doing these things by accident or on a
page's instructions, and it is a courtesy layer for the CLI, not a barrier
against an agent running as you:

- `~/.monoagent/extension.token` is readable by anything running as you
  (mode 0600, same user). With it, a process can drive the bridge protocol
  directly: post a `set_binding` to `/monoagent/relay`, or connect to the
  websocket as a browser and claim a profile in a `binding` frame, so an
  endpoint it runs would receive that profile's browser actions. The gates
  above don't cover that path.
- The `pair --open` gate is cosmetic against an agent with a shell, which
  can start a bridge of its own and open the pairing page itself.
- An agent with a shell can also edit the files these commands manage.
- "Operator-only" is a courtesy layer against a same-user agent: it can edit
  `~/.monoagent/extension.pending.json` (which holds only hashes, but also the
  claim states) or read `~/.monoagent/extension.token`, and it can read the
  confirmation code off a screenshot. The people it really protects are other
  OS users and other local software without your user's file access.

So it is not a sandbox: it stops mistakes and planted instructions that
go through the CLI.
### Pairing a browser without typing a token (the claim)

A freshly installed extension has no token. While this machine is signed in
to monoes.me it asks `GET /monoagent/status` and then
`POST /monoagent/pair/claim` on the bridge. What that yields depends on
`extension autopair ask|auto|off` (setting `extension.autoPair`, read by the
bridge on every request). `MONOAGENT_EXTENSION_AUTOPAIR` in the bridge's
environment can only make it STRICTER than the stored setting (off < ask <
auto; an unset setting is ask), and a value it does not recognise means `off`:
`auto` in the environment never loosens a stored `ask`. `extension autopair`
prints the mode in force and where it comes from (default, settings or env).
An agent's shell cannot change the mode, approve or deny (operator-only; see
the limits below).

**Why `ask` is the default.** The `Origin` header a claim relies on is a
string the caller chooses, not a credential. Any local process
(`curl -H 'Origin: chrome-extension://x'`), any OTHER OS USER on the machine
(loopback is shared between users) and any other browser extension with host
permission for `127.0.0.1` can send an extension origin. A per-browser token
grants what `~/.monoagent/extension.token` (mode 0600, readable only by you)
grants: documents, vault, tasks, chat, form fill and the browser-control
commands (page text, cookies, scripts). So an Origin check alone must not
hand that out.

- **ask (default).** The claim only records a pending request (an id, the
  browser label, its origin; two minutes to live; at most five waiting, and at
  most one per instance id: a repeat from the same instance replaces its own
  waiting request; a full queue refuses new ones and drops none) and answers
  with the id, a poll secret (32 random bytes; only its SHA-256 is stored,
  compared in constant time) and a short confirmation code (`ABC-123`, derived
  from the id and the secret; only a hash is stored). The browser's sign-in
  screen shows the code. No token exists until you approve with that code:
  `monoagentcli extension approve <id> --code ABC-123` (asked for at a prompt
  in a terminal), or on the card in the app's Human in Loop queue (type the
  code in its code field). The CLI and the card name the request (id, browser,
  origin, instance) but never show the code, so you approve the request THEIR
  browser is displaying: a forged request, whose code only its sender sees,
  cannot be approved by mistake. There is no "approve the newest": with no id,
  `approve` and `deny` act only when exactly one request is waiting, and with
  several they list them and refuse (the newest is the forger's to choose).
  `extension deny --all` clears the queue; `extension browsers list` says how
  many are waiting. Approving prints the instance id and warns when it would
  replace the token of a browser that is already paired. `extension deny`
  refuses. The extension polls `GET /monoagent/pair/claim/<id>` with the secret
  as a bearer; the poll that sees the approval receives the token once and the
  record is destroyed. A wrong id, a wrong secret and another origin's request
  all get the same 404. An approval made on the card's row by any route other
  than `hil approve` or the app does not approve the claim. Everything a
  browser supplies (label, origin, agent) has control and format characters
  (terminal escapes, bidi overrides U+202A-202E and U+2066-2069) stripped when
  it is stored, and the CLI prints it quoted. The pending and browsers files
  are changed under one cross-process lock (`extension.lock`, flock or
  LockFileEx), so the bridge and the CLI cannot undo each other's write; a
  decided claim stays in the file, decided, until its browser collects it.
  Protects against: other OS users, other extensions, scripts and anything
  else that can reach the loopback port but cannot also read your browser's
  screen. It does NOT protect against a user who approves a request they did
  not start and types a code they were given; and it does not stop a forger
  from filling the five slots (a nuisance, cleared with `deny --all`). A
  process that can drive your approvals (your own shell) already holds your
  account's power.
- **auto (opt-in; single-user machines only).** The claim returns a token at
  once, with no step and no check. Any extension, local process or other OS
  user on this machine that can reach the bridge gets a token with full
  powers while you are signed in, and 32 forged claims fill the paired-browser
  table and permanently block legitimate pairing until you revoke them
  (`extension browsers revoke`). Use it only on a machine only you use, only
  while you are watching it, and switch back to `ask` afterwards.
- **off.** No claims; every browser needs `extension pair`.

In every mode, a claim or poll is refused (403, nothing recorded or handed
out, a warning logged) unless: the TCP peer is loopback and the `Host` header
is a loopback name (a DNS-rebound page sends its own domain); the `Origin` is
`chrome-extension://` or `moz-extension://` (a claim with a web or no origin
is refused; Chrome sends no `Origin` on the extension's GET poll, which is
accepted because the secret is its proof, but a web origin on it is refused);
`Sec-Fetch-Site` is not same-origin/same-site and `Sec-Fetch-Mode` is not
`navigate`; the account guard says signed in; the origin made at most five
claims in the last minute (per origin, so forged claims cannot use up the real
extension's quota); and, when `MONOAGENT_EXTENSION_IDS` lists extension ids
(comma-separated; an unpacked development build has its own id and must be
listed), the origin is one of them. The id list narrows who can ask; it is
still a header, so it is not a substitute for `ask`.

Per-browser tokens are recorded (a SHA-256, a label, the origin, when, capped
at 256 characters each) in `~/.monoagent/extension.browsers.json`, listed with
`extension browsers list` and cut off alone with `extension browsers revoke
<id>`; a live socket is closed within a few seconds of the revoke. The shared
`extension.token` is never handed out; `extension reset` still ends it.
At 32 paired browsers new pairings are refused until you revoke one: no valid
token is ever evicted. A claim that names an instance id already paired cannot
replace or delete that entry: it needs the entry's own token (a browser
rotating its token) or, in `ask`, your approval of a card that says so.

Remaining limits:

- A forger can fill the pending queue (five slots, two minutes each) and so
  delay the real extension's request; it can never obtain a token. Deny or
  wait.
- The pending-claim and browsers files are shared by the bridge and the CLI
  as plain files with atomic renames and no cross-process lock; an approved
  claim can mint only once (its entry id is the claim's), but two processes
  racing on the same file can lose a write, which at worst means asking again.
- A process running as you can already read `extension.token`, so the claim
  adds nothing for it.
- `/monoagent/status` says, to any loopback caller with no or an extension
  `Origin`, whether an account is signed in, whether a browser is connected
  and the pairing mode. It adds no CORS header, so a web page cannot read it.

### What the app shows of an agent's commands

Coder chats and orgs show each tool call an agent makes: its command line,
its input and its output, on a card, in an org bubble's tool rows, on the
stage and in the background-process banner. Two rules keep secrets off
those surfaces, and they are not equally strong.

- **Hiding is the guarantee, for MonoAgent's own secret commands.** A
  call that names `monoagent` (in any case) together with `secret`,
  `vault`, `--value` or `--field`, or that runs `profile info set`,
  `profile info get --reveal`, `account login`, `library login`,
  `jev key set`, `connect set-oauth-client`,
  `connect get-oauth-client --reveal`, `connect save`,
  `connect oauth --stdin-json`, `api key create` or `extension pair`,
  shows no command line, input or output at all, however it is wrapped
  (`bash -c`, `eval`, a variable, a chain, a heredoc or pipe into it). A
  vault card names the entry only for `get`, `reveal`, `update` and `rm`.
- **Masking is best effort, for everything else.** Values after a
  credential-shaped name (`--token`, `API_KEY=`, `"apiKey":`,
  `Authorization:`, `?access_token=`, `curl -u user:`, `mysql -p` and
  more) and well-known token shapes (`sk-`, `ghp_`, `xoxb-`, `AKIA`,
  JWTs, Stripe, npm, GitLab, SendGrid, private key blocks, webhook URLs,
  a URL's password) read `•••` in command lines and inputs; output gets
  the token shapes. So does the value of a name/value pair whose label is
  a credential's (`{"name": "password", "value": …}`, a Kubernetes env
  entry, helm's `name=password,value=…`), every `--input` of
  `record verify` and `record replay`, and the text `browser type` types
  into a field whose selector names a credential (`type=password`,
  `#pw`, `[name=otp]`, `.card-number`); text typed into a selector that
  names no credential (`#field3`, `.form-control`, `input:nth-child(2)`)
  is shown as typed, by design. Token counts and key-like names that are
  not secrets (`--max-new-tokens`, `token_budget`, `--cache-key`) stay
  readable. It catches known names and
  shapes; it cannot promise to catch every secret. What it cannot catch:
  a secret piped in on stdin (`echo … | docker login --password-stdin`,
  `sudo -S`, `chpasswd`, `htpasswd -b`), a here-string, a positional
  argument with no telling name, a base64 blob or a long hex string, and
  a secret written into prose (a task's title or notes, a comment, an
  edited message). Long hex and base64 are left alone on purpose: they
  would also hide commit hashes and ordinary data.

## Network use, telemetry and crash reporting

**No analytics, no usage counters, no device identifier.** Mono Agent does not
report what you run, which workflows you have or how often you use it. A
monoes.me sign-in has been required since 2026-10-11 (UTC) (`enforce_from` in
`account status`), so it does contact monoes.me, and it checks for updates. The
sign-in requests below happen on their own, without a command you typed: the
refresh while a process runs and the one-time exchange of an older library
login. This is the complete
list of the situations in which Mono Agent makes network requests:

1. **monoes.me, to sign in and stay signed in.** `account login` (and `library
   login`, the same sign-in) runs the OAuth 2.1 authorization-code exchange with
   PKCE: the client id `monoagent`, the requested scopes, the resource
   `https://monoes.me/api/monoagent`, a loopback redirect address, and the code
   with its verifier; with `--email` instead, the email address you type (with the
   client id and scope), and then the address again with the code mailed to it.
   `account logout` revokes the refresh token (it sends the token and the client
   id). A refresh follows while a `monoagentcli`
   process runs: a long-running one (the daemon, a server, the desktop app) at
   about half the token's lifetime, a short command only when the token has under
   five minutes left. It sends the refresh token, the client id and the
   resource. On the first gated command, a monoes.me
   library login left by an older release is exchanged once for this sign-in (its
   refresh token, the client id and the resource); if monoes.me does not allow
   that, nothing changes and you sign in once more. That older login is sent
   to monoes.me (exchanged, refreshed, used for a library request, or revoked
   by `library logout`) only from your own install: `HOME` is your OS account's
   home directory and the database is `~/.monoagent/monoagent.db` in it. A copy
   of the database, used from another `HOME` or with `--db-path`, sends nothing
   and keeps the login, and the CLI prints why. When the system cannot say
   which home directory your OS account has (on Linux, an account that is not
   in `/etc/passwd`, such as an LDAP account or a container running under an
   arbitrary user id), the login is not sent either: sign in with
   `monoagentcli account login` instead. Every host counts as monoes.me here
   except a loopback address (a development server on your machine). Each
   exchange prints one line saying what became of the login, never the token
   itself. monoes.me learns which
   account, when, and the IP address the request comes from. Nothing else is
   sent: no device identifier, no usage data, no workflow content, no file path.
2. **monoes.me, for the library** (`library list`, `show`, `install`, `publish`,
   `update`), only when you run those commands, with your signed-in token; the
   host is `MONOES_BASE_URL` or https://monoes.me, and the token is sent only to
   the host that issued it. A library upload sends the artifact you choose to
   publish.
3. **Updates:** `update`, `update --app`, `doctor --deep` and the desktop app (at
   start and every 24 hours) fetch the signed release manifest from monoes.me or
   the releases repository, and fall back to GitHub's release metadata where the
   build still allows it. The server sees the IP address and Go's default
   `User-Agent`; no account or device identifier is sent. Downloads are verified
   by SHA256, and by Ed25519 signature once a signing key is pinned; without a
   pinned signing key a manifest cannot be trusted and the GitHub fallback is
   used.
4. API calls made by workflows you run, against services you configured (HTTP
   nodes, service nodes, browser nodes, etc.), and commands you explicitly invoke
   that talk to an external service, for example `login` (OAuth flows for
   third-party platforms).
5. Opt-in crash reporting, below.

**Without a connection**, Mono Agent keeps working for
24 hours after the last successful contact with monoes.me (counted from the
signed issue time of the newest token). Then every gated command refuses with
exit code 4 (`login_required`) until it can reach monoes.me again; `version`,
`help`, `ref`, `update`, `doctor`, `setup` and the `account` commands always work,
and a daemon or server (`daemon`, `httpapi`, `mcp`, `extension serve`) stays up
and refuses its work. A blocked or revoked account is locked at the next refresh,
within about an hour.

**Crash reporting is local by default.** If the CLI crashes, it writes a
crash report to a file under `~/.monoagent/crashes/` on your machine —
nothing is transmitted. Filing a crash report to GitHub happens only when
**both** of the following are true:

- the environment variable `MONOAGENT_CRASH_REPORT=1` is set, and
- the `monomind` CLI is installed and on `PATH`.

Without either condition, crash data stays in the local file. There is no
automatic network fallback for crash reporting.

Everything else — workflow definitions, execution history, the secrets
vault, CRM data, and crash reports — stays on your machine.

### The monoes.me sign-in on disk

One sign-in per OS user, shared by every profile, in `~/.monoagent/account/`
(directory mode 0700, whatever `--db-path` says). Nothing is written there until
you sign in: `session.json` (0600) holds the signed access token, valid for about
an hour, and display data, readable without the keychain so that a check never
triggers a keychain prompt; `refresh.enc` (0600) holds the refresh token sealed
with AES-256-GCM under a key from the OS key store or, where there is none and
`MONOAGENT_ALLOW_FILE_KEYRING=1` is set, the file keyring (see
[File-based keyring fallback](#file-based-keyring-fallback-weaker-posture)), read only by a refresh; and `session.lock` (0600) keeps two
processes from refreshing at once. `account logout` deletes the token and the
refresh token and keeps a `session.json` that holds no token, only the highest
time this machine has seen, which is what stops setting the clock back from
switching the requirement off. The access token is verified on this machine
against signing keys built into the binary: there is no network call per command.
The production signing key is pinned (a fixture token signed by the production
key is verified in the tests), so a production machine session is verified on this
machine.

The key that seals the refresh token is an item of its own in that key store (in
the OS keychain, under the vault's service name `monoagent-vault`). In a file
keyring it is listed by `secret keyring status` as the id `monoes..account`. It
exists only after an explicit `account login` creates it.

The account gate that uses this session is **live** since 2026-10-11 (UTC): a
gated command without a valid sign-in exits 4 with `login_required`. A binary built with `-tags devaccount` trusts a development
signing key that anyone can sign with; it is for testing only, and CI's release
guard (`scripts/check-release-tags.sh`) fails a release whose binaries carry that
tag.

### One sign-in per machine, never copied

Sign in once on each machine. Never copy or restore `~/.monoagent/account/`
between machines or from an old backup or disk image, and never bake a sign-in
into a Docker image or a container snapshot: keep it in the container's volume.
monoes.me rotates the refresh token at every refresh and treats one that was
already used as stolen: it ends that sign-in. The refresh token names the folder
it was sealed for, by its identity on the disk (by its path where the disk gives
none), so a copy of the folder is never renewed, sent or revoked, wherever it is,
even put back where the original was: it reports `keyring_unavailable` and has to
sign in on its own. The folder itself may be renamed or moved on the same disk (a
renamed home directory, a volume mounted at another path) and keeps working;
moved to another disk, or restored from a backup, it has to sign in once more.
Only a disk image restored block for block keeps the folder's identity: once
presented, such a stale copy signs out the original and every copy, and each of
them then has to sign in again. Machines that signed in on their own keep
working. These checks guard against accidents, such as an agent copying the
folder: a program that runs as you and can call the key store can seal any
folder's name, or unseal the token itself (see "A copy of `~/.monoagent` and the
key store"). A sign-in made by a release before this one names no folder: it is
read only in your own `~/.monoagent/account`, and its next renewal, within the
hour, ties it to that folder. If the system has no home directory for your
account (on Linux, one that is not in `/etc/passwd`: an LDAP account, a
container's arbitrary user id), that cannot be checked: sign in once more after
upgrading. The CLI says so.

Do not copy `~/.monoagent/monoagent.db` either. Its vault can still hold a
library login from an older release, and any copy made by the same OS user can
open it. The CLI sends that login to monoes.me only from your own install (see
"Network use" above). Setting `MONOES_BASE_URL` does not keep a copy away from monoes.me:
in a default build it changes where the library's requests go, not where you sign
in. Only a `-tags devaccount` build changes the sign-in host (spec D24).

### When a sign-in is locked

When the gate is enabled, the reason `refused` means that monoes.me answered
`invalid_grant` to a refresh: the account was blocked, the sign-in was revoked,
or a refresh token that was already used was presented (a copied sign-in, above).
The machine locks at its next refresh (within about an hour of a block), work in
flight is cancelled, and `account login` is the way back in; a blocked account
stays refused until monoes.me unblocks it. Every other failure, such as no
network, a server error or any other OAuth error, is not a refusal: the machine
keeps working until it has gone 24 hours without reaching monoes.me (`expired`).
An unknown refresh outcome is retried for 240 seconds; then this machine drops its
refresh token and reports `unconfirmed`, and its other independent installs are
untouched. A clock set back can lock work (`clock_rollback`); a freshly verified
sign-in resets the clock guard.

Signing out of the monoes.me website does not sign out an install. An install keeps
its own sign-in until you run `account logout` on it, monoes.me blocks the account
or revokes the sign-in, or it goes 24 hours without reaching monoes.me.

### The sign-in requirement is a product check

Mono Agent checks for a valid sign-in before it runs a command, and again while a
daemon, an API, an MCP server or the extension bridge runs. This is a check in the official builds, not a security boundary: a
binary you build or patch yourself can remove it, a copy of the session files
works on another machine for a while (it counts accounts, not devices) until one
of the two copies presents a refresh token the other has already used, which ends
that sign-in on both machines (see above), and releases before the enforcement
release do not have it. A bypass of an official build (a token that verifies when
it should not, a gated door that stays open) is a vulnerability: report it as
described above. Removing the check from a build of your own is not. The license
forbids it (section 4 of `LICENSE`).

## Release governance

`release.yml` triggers on every push to `master`, and its `release` job
(which publishes the GitHub Release) targets a `release` GitHub Environment.
This is only an enforced approval gate once the following are configured in
repo settings — until then it is a no-op:

1. **Settings → Environments → New environment**, named `release`, with
   **Required reviewers** set to at least one maintainer.
2. **Settings → Branches → Branch protection rule** for `master`: require
   pull request review before merging, require status checks to pass
   (`test`, and the `mcp-pin-guard`/`vuln-scan` CI jobs), and disallow
   force pushes and branch deletion.
3. **Settings → Tags → New rule**: protect `v*` so a published release tag
   cannot be silently moved or replaced.

### Update manifest signing key

The Ed25519 key that signs the update manifest (`manifest.json.sig`) is stored
as the `RELEASE_SIGNING_KEY` secret of the `release` environment, and CI signs
the manifest. This replaces the earlier model in which only the owner signed
locally; local signing (`release-manifest sign -keyring`) remains as a
documented break-glass path. See `docs/release-runbook.md` for setup,
rotation and incident steps.

How the key is held: GitHub releases an environment secret only to a job that
targets that environment and passes its protection rules. The `sign-manifest`
job runs only for a push to `master`, has `contents: read` only, exposes the key
to a single step, and signs only after recomputing the sha256 and size of every
built asset and checking that the version is the one being released and not
lower than the latest published release. Clients pin the public key; the
release workflow never prints the key.

What this protects against: a compromised build or dependency step that tampers
with an asset after the manifest is generated (the signing job refuses on a
checksum mismatch), a forked or pull-request workflow (no access to environment
secrets), a push from another branch, a replay of an older version, and a
leaked workflow log (the key is never printed).

The pre-sign check proves the manifest matches the build artifacts; it does not prove the
build was not compromised, since a tampered build step yields artifacts and a manifest that
agree with each other. The `publish-releases-repo` job re-verifies the signature and every
uploaded file against the signed manifest, which stops tampering between jobs but not a
compromised build. The environment approval and branch protection (with code-owner review
for `.github/CODEOWNERS` paths) are the real controls.

What it does not protect against: anyone who can change the workflow on
`master` **and** approve the `release` environment can sign anything, because
the key is available to that job by design. The controls are therefore the
branch protection on `master` (item 2 above) and **required reviewers on the
`release` environment with deployment branches limited to `master`** (item 1).
Do not add a reviewer you would not trust with the signing key, and do not turn
off self-review prevention if there is more than one maintainer. If the secret
may have leaked, rotate it and ship a release that unpins the old key
(runbook, "Incident").

## Verifying a release

Releases are published at https://github.com/monoes/mono-agent/releases.
Every release carries `SHA256SUMS.txt` next to the binaries, and a
`manifest.json` with its signature `manifest.json.sig`. `update`,
`update --app` and `install.sh` verify the manifest signature against the
public key pinned in the binary, then every download against the checksums in
the manifest, and install nothing on a mismatch. To check a file by hand:

```bash
shasum -a 256 --check --ignore-missing SHA256SUMS.txt   # macOS; sha256sum on Linux
monoagentcli release verify manifest.json manifest.json.sig
```

There is no build provenance attestation to check: releases are built in a private repository, and GitHub provides
attestations for private repositories only on GitHub Enterprise Cloud. The
binaries do not yet carry a personal code-signing identity (see below), so on
macOS/Windows you will still see an unidentified-developer warning until that
lands.

### Code signing (in progress)

The macOS CLI binary is currently signed ad hoc with the hardened runtime (`codesign --options runtime --sign -`), which
satisfies Gatekeeper's local-execution requirement but carries no verifiable
publisher identity, and Windows binaries are not yet Authenticode-signed.
Real Developer ID signing + notarization, and Authenticode signing, are
tracked as a follow-up (MA-07) pending the relevant certificates. Once
available, the required repository secrets are:

| Secret | Purpose |
| --- | --- |
| `APPLE_CERT_P12` | Base64-encoded Developer ID Application certificate (.p12) |
| `APPLE_CERT_PASSWORD` | Password for the above .p12 |
| `APPLE_TEAM_ID` | Apple Developer Team ID |
| `APPLE_NOTARY_APPLE_ID` | Apple ID used for notarization |
| `APPLE_NOTARY_PASSWORD` | App-specific password for that Apple ID |
| `WINDOWS_CERT_PFX` | Base64-encoded Authenticode code-signing certificate (.pfx) |
| `WINDOWS_CERT_PASSWORD` | Password for the above .pfx |

## Resource limits

**Importing or running a workflow is equivalent to executing code** —
workflows can run shell commands, inline JavaScript, and HTTP calls, so
only import workflows from sources you trust. To bound the blast radius of
untrusted or misbehaving workflows, runs are resource-capped: HTTP node
bodies are limited to 64 MB by default (configurable); `core.code` nodes
run with a 30 s default timeout (configurable) and process at most 10,000
items of 16 MB per item — an engine-level memory ceiling is not yet
enforced by the vendored JS runtime; `system.execute_command` output is
capped at 10 MB per channel (stdout and stderr). Stored outputs are
persisted in full but display-truncated at 4 KB.

## Org automations and autonomy

Org roles are AI agents run by monomind. Letting them call workflows, and
letting a model resolve their approvals, crosses a trust boundary. The
layers, from strongest:

1. **Grant rows.** Which automations a role may run, how often, and
   whether a call needs a decision live in mono-agent's database
   (`org_grants`). The org file is only a display copy: any role whose file
   write reaches `.monomind/` can edit it, so saves and the daemon strip
   grants, tool providers, and endpoints no row backs, and never create
   rows from the file.
2. **Grant-mode MCP server.** `mcp --grant` serves only the granted tools,
   takes its scope from the grant row, refuses a grant used by another org
   or role (checked against monomind's `MONOMIND_ORG_NAME`/`ROLE`, which it
   requires — an unset variable refuses the call rather than skipping the
   check), caps calls per run and per day — a cap it cannot read refuses
   too — and returns redacted, size-bounded outputs.
   It runs no engine and holds no vault access; the daemon runs the
   workflow.
3. **monomind policy.** `denyTools`, `approvalTools` (managed by mono-agent
   for `approval: required` grants and `org_start`), budgets, and the
   message fence, which is turned on the first time an org gains an
   automation.
4. **Automation-role endpoints.** `POST /org-endpoint/{id}` on the daemon's
   API. The id is a 130-bit capability, checked in constant time;
   non-loopback callers need the endpoint's 0600 credential file (on
   Windows: owned by the daemon user, SYSTEM or Administrators, with no
   ACE granting anyone else access). A
   delivery runs only after the same `messageId` appears on the org's bus
   addressed to that role, so a role that read the id from the org file
   cannot POST around `org_send`. Rotation keeps the old id for 5 minutes.
5. **Loop control.** Every crossing carries a trace; hops a header claims
   are never trusted below the recorded count, and repeat limits key on
   the target, which a caller cannot forge. `run_config`'s `max_hops` and
   `max_repeats` are read from the org file, so they are clamped to hard
   ceilings — raising them there cannot switch loop control off.
   A chain survives a trip out over HTTP and back through a webhook: the
   HTTP request node sends the run's chain as `X-Monoagent-Trace`, signed
   with a per-machine key (`~/.monoagent/trace.key`, 0600,
   `internal/tracesig`), and the webhook server continues the chain only
   when that signature verifies, recording the crossing (`webhook_in`) and
   refusing the request (429) past the hop limit. The signature covers
   chain, hop and an expiry (one hour), so a replayed token cannot lower
   the hop and stops verifying after the hour. A webhook
   body can never set the chain: the server drops the reserved
   `monoagent_trace` field and gives a run without a verified header a
   fresh chain. The token names only a chain and hop. It goes only to
   this machine (loopback, or the host of `MONOAGENT_WEBHOOK_ADDR`; with a
   wildcard bind, any of the machine's interface addresses or its host
   name, compared and never resolved) unless
   a node sets `propagate_trace`, and never follows a cross-host redirect:
   a third party holding one could push its chain to the hop limit. A
   `webhook_in` crossing takes the signed hop as is (a run's fan-out
   requests are siblings at hop + 1; a loop's return is one hop deeper each
   round); the signed hop is the deeper of the run's and the one its item
   reached on the chain. It is held to the `max_hops` of the org the chain
   started in (the defaults for a chain no org started), and to 200 runs of
   one workflow a minute (429 past that), so replaying a token cannot grow
   `org_bridge_calls` without bound: past either limit only the first
   refusal per minute is recorded. `trigger.org` runs are admitted too
   (`org_event`, or `event_start` on a fresh chain), under the same
   200-a-minute limit and the org's `max_hops`, so a loop that goes out
   through a role's tool event and comes back by a path the ledger does not
   record still climbs. A tool event that is not a granted call (Bash, file
   reads) continues the role's chain only when that workflow's own
   trigger.org run started the chain; an audit workflow is not put on a
   role's chain, and `org_event` rows never raise a chain's depth. The key
   is refused if other users can read it; delete it and restart the daemon
   to rotate it.
6. **Workdir confinement of automation file paths (C-46).** Automations run
   in the daemon, outside monomind's per-role workdir confinement. So the
   grant handler puts the calling role's workdir (computed from the
   profile root in the database and the org's `run_config.workspace`,
   never from the provider's environment) in the run's trigger data as
   `org.workdir`, under `org`, which the role's arguments cannot set. An
   automation role's run gets the sending role's workdir the same way. The
   engine confines every file-touching node of such a run
   (`internal/fsconfine`): spreadsheet read/write, write file, image nodes
   and their output folders, image vault save, email and Outlook
   attachments, Slack and Telegram uploads, FTP local paths, YouTube and
   Drive uploads, browser upload steps, and Gemini reference images. A
   path is resolved relative to the workdir, `..` and symlinks are
   followed, a dangling symlink is refused, and anything that resolves
   outside the workdir is refused with `path escapes org workdir` before
   the file is opened or any request is sent. Runs without `org.workdir`
   behave exactly as before. `org automation list`, `org grant add|list`,
   `org effective-tools`, and the GUI grant dialog flag workflows whose
   file nodes take their paths from the run's input.
7. **Autonomy.** The level, decider, tiers, and policy are enforced from
   `org_autonomy`; an edit to the org file can only lower the level. Tiers
   are computed from the grant row and the workflow's nodes before any
   decider runs, agent-written text sits inside an untrusted fence in the
   decider prompt, a decider never resolves its own request, and every
   routed item is recorded in `org_decisions`. `org autonomy pause` drops
   an org to manual at once.

**Known gaps (accepted for now):**

- **Bash.** A role allowed Bash can run `monoagentcli` with your rights and
  bypass its grants. A role's first grant pre-fills `denyTools: ["Bash"]`
  and the GUI warns when Bash is re-enabled. The org/role check in layer 2
  is no barrier to it either: grant ids are not secrets (every role's
  provider args in the org file carry one), and a role that can run
  commands can also set `MONOMIND_ORG_NAME`/`ROLE` to match a sibling's
  grant. That check catches a copied id and a misconfigured provider, not a
  role that already has a shell.
- **File paths in automation input (C-46), what confinement does not
  cover.** Layer 6 holds file nodes to the role's workdir; it does not
  hold `system.execute_command`, which runs a shell command with your
  rights — a granted workflow that passes its input to a command can reach
  any file, as a role with Bash can (the grant dialog names such nodes).
  A process that swaps a directory for a symlink between the check and the
  open is not stopped. The workdir is read from the org file, so a role
  that can write that file (workspace `repo`) can widen it — the same
  edit widens its own confinement at the next org start. A message from
  an org this profile folder cannot resolve is held to the profile folder,
  and a message from a person is not confined.
- **Level full.** At `full` a model decides irreversible items (gates,
  grants whose workflows send email or post) with no person involved. The
  fence and tiers reduce prompt-injection risk; they do not remove it.
  Choose `full` only for orgs whose automations you would accept being run
  on a wrong decision.
- **Cross-root messaging.** Orgs under the same profile folder can always
  message each other. Restricting messages between profile folders needs
  monomind with capability `org-federation`.

## Send approval cards

A send that an agent, or a workflow you never approved, prepares waits as a
`send:` item in the Human in Loop queue (`internal/hilsend`). When its words
aren't a message you could edit, its card shows each field it sends, the
recipient-like ones first and in full up to their share, and then every
address in the payload the fields don't show, with its path: email
addresses, links and tel:, sip: and sms: URIs, @handles and Matrix ids,
channels and Slack ids, phone numbers, long numbers, UUIDs, IP literals and
bare domains (`internal/apipkg/FORMAT.md` §3). A card that can't show all
of that, or whose payload is too large to check, can be rejected, never
approved.

**Known gaps (accepted for now):** the card reads shapes, not meaning, so
it doesn't find an address that a reader wouldn't take for one either:
spelled out or otherwise obfuscated (`evil at x dot com`), percent-encoded
(`evil%40x.test`), in base64 or HTML entities (`evil&#64;x.test`), written
with a fullwidth `＠`, or split over two leaves the service joins
(`{"user": "evil", "domain": "x.test"}`). Such a payload still shows its
fields, cut with a mark and never left out silently. Also: a domain whose
last label is a file name's ending (`evil.py`) and a number inside a word
on both sides (`ab5551234567cd`) aren't read; 10 or 13 digits alone under a
time-like key that read as a time from 2001 to 2100 count as a Unix time;
and an address that one recipient-like field shows isn't listed again from
another.

## Webhook trigger surface

The webhook trigger server (`internal/workflow/webhook_server.go`) binds
`127.0.0.1:9321` by default — loopback-only, same-user trust, plain HTTP
with no extra ceremony (the same trust model the Chrome extension bridge
uses). Override the bind with `MONOAGENT_WEBHOOK_ADDR` when triggers need
to be reachable from another machine (a Docker container, a VM, a LAN box).

**Any non-loopback bind is always served over TLS — there is no way to
serve a remote bind in the clear.** Webhook payloads can carry
caller-attached auth headers/tokens, so an unencrypted remote listener
would leak them on the wire. If no certificate is configured, the server
auto-generates and caches a self-signed one (ECDSA P-256, ~13 months
validity, covering `localhost`/`127.0.0.1`/`::1` only) under
`~/.monoagent/webhook-tls/` (key file mode 0600) on first bind, and reuses
it until it expires. Callers connecting by LAN hostname or IP must skip
certificate verification against this self-signed cert, or trust it
explicitly. For a real certificate, set both `MONOAGENT_WEBHOOK_TLS_CERT`
and `MONOAGENT_WEBHOOK_TLS_KEY` to its path/key path — setting only one of
the pair is a startup error rather than a silent fallback.

**TLS protects the payload in transit; it does not authenticate the
caller.** The webhook server itself has no built-in auth beyond TLS — the
existing pattern is workflow-level: give each webhook trigger node an
`auth_header`/`auth_token` pair or an `hmac_secret` (see
`examples/README.md`), which the server checks per-request
(`X-Hub-Signature-256` for HMAC, or a constant-time comparison for the
static header token). Anyone who can reach a remotely-bound port and
guesses the trigger's `<path>` segment can otherwise fire it. Pair this
with `MONOAGENT_WEBHOOK_ALLOWED_ORIGINS` (a comma-separated origin
allowlist) if browser-based callers need to reach it; without it the
server emits no CORS headers at all, so cross-origin browser requests are
blocked outright — see [Runtime environment variables in
AGENTS.md](AGENTS.md#runtime-environment-variables).

## Task board

`monoagentcli task` keeps a task board per profile (tables `tasks`,
`task_events` and `task_board_rev`; a profile's board is deleted with
it). Task text can come from outside: the title and notes of a task
added from a web page or another app are untrusted data, and an AI agent
that works a task acts on them. The defences:

- **A gate before an agent sees a task.** Everything captured, or
  created by an agent, lands in the Inbox, which agents do not see
  unless they name it. Only the operator approves a task into Ready,
  where agents may claim it (an agent's `release` can put back into
  Ready only a task it holds), and only the operator moves one to Done.
  An agent's `finish` goes to Review. An agent cannot edit a task's
  text, so what the operator approved is what it reads.
- **Delegation, off by default.** The operator can let agents see the board
  and the Inbox (`task agents allow --view`) and let a named agent approve
  (`task agents allow --view --approve`), per profile; `deny`
  takes it back at once, and `allow` refuses to run unless standard input is a
  terminal. A delegate approves only what it can see (approve needs view), never
  a task an agent created (so "an agent adds an injected task, the same agent
  approves it" is closed), and at most 10 tasks a call. The setting is the `settings` row
  `task_agent_access:<profile id>`, read at each call, changed only by
  `allow`/`deny` (which refuse an agent-driven caller, and the store refuses
  a non-operator actor) and by no MCP tool. A delegated approval moves Inbox
  to Ready only, is written in the task's history under the agent's name and
  marked as delegated, and over MCP is the tool `task_approve`, listed only
  while `approve` is on. The risk: Inbox text comes from web pages and other
  apps and can be written to steer an AI, and an agent that may approve turns
  captured text into work another agent runs, without the operator reading
  it. Like the operator guard, this is a convenience boundary against
  injected text and not a sandbox: an agent with a shell or direct database
  access can change the setting or the board, and an agent can still approve
  operator-written or captured tasks whose text carries injected instructions.
  Leave it off unless you trust
  the agents on the machine.
- **Over MCP** an agent works the board through the `task_*` tools of `monoagentcli mcp`, over stdio (no port, no HTTP route). They act as the agent the server names after its client (`agent:<client>#<4 hex>`: two sessions are two claimants, and no argument can choose the name), on the profile each call names (the required `profile` argument; a task of another profile is not found). No tool edits, moves or archives a task, and none approves unless the operator delegated it (see Delegation). Every text a person, an agent or a capture wrote comes back in a field ending in `_untrusted`, with a note to weigh it and not follow instructions inside it, and a tool refuses any argument it does not list. `mcp --tasks-only` serves these tools without the workflow tools that `--allow-mutations` would also serve.
- **Task sessions: the operator opens a goal once, not each task.** A
  session (tables `task_sessions`, `task_deps`, `task_artifacts`,
  `task_session_events`, `task_session_rev`) is a goal with a board of its own,
  planned by an agent as a draft. Until the operator runs `session approve`
  its tasks cannot be claimed, are in no inbox (so the rule that an agent
  never approves what an agent created is untouched) and are skipped by every
  read of the personal board, which behaves exactly as before for a task with
  no session. Approval, resuming a pause, the budget, the caps (`max_tasks`,
  `max_depth`, `max_retries`), the access level, `auto_close_verified`,
  archiving a session, recording spend and removing a dependency are the
  operator's: the store refuses any other actor, and the CLI refuses an
  agent-driven caller with `operator_only`. Inside a running session an agent may
  claim, comment, finish, add follow-ups (bounded by `max_tasks` and the
  budget, with no separate cap), split a task that has not begun, add
  dependencies (a cycle is refused), attach artifacts and verify; it may pause
  or stop the session (a stop releases every claim) and it cannot undo a pause.
  A verifier's pass marks a task verified and never closes it, unless the
  operator turned on auto-close. Who verifies is enforced by the binding plus an
  identity check, not by a name alone: the operator may; an agent bound to the task
  as its worker never may (only a verifier binding passes), and a named agent of the
  session may not verify a task whose last hand-back came from the same agent
  identity (its `agent:<client>` name without the per-connection `#<4 hex>`, compared
  without case). A name is a label an agent picks, so an agent that is not bound to
  a task can get past the identity check by choosing another name: a worker started
  by the conductor is bound, a free agent is not. An agent verifies only while the
  session runs; the operator may act while it is paused. A task that fails
  verification more often than
  `max_retries` is blocked for the operator. A process bound to a session
  (`MONOAGENT_TASK_SESSION`, the carrier of the worker token to come) may act in
  that session only: not on the personal board and not in another session, in
  the CLI, the store and over MCP. That includes the operator-enabled delegation
  (`task agents allow --approve --view`): a bound worker cannot approve or view the
  personal board through it, the `task digest` hook shows it nothing of that board,
  and a delegated approval never opens a session's task (only `session approve`
  does). That variable is a label, not a credential: an agent that sets or unsets
  its own environment can change it. **An agent that scrubs its own environment (no
  agent marker, no `--as`) is treated as the operator by the CLI** and can run
  `session approve`, `session resume` and `session budget`: it can open its own plan,
  resume a pause and raise its own budget. That weakness predates sessions and has
  higher stakes with them; the guard stops accidents and injected text, not an agent
  that works around it on purpose. A session's
  goal and a task's acceptance criteria are text an agent wrote and come back
  as data (`_untrusted` over MCP, a notice in text), and a session's tasks are
  deleted with its profile. A session that has spent its budget hands out no tasks.
  **A mission's verifier** is started by the lead (`mission_verify`) as another agent
  than the executor, read only, with `MONOAGENT_TASK_ROLE=verifier` next to the task
  binding: in the store that actor may verify, comment on (once the task is in Review)
  and attach evidence to its one task, and is refused claiming, finishing, releasing,
  splitting, adding or writing session events; a verifier without a task is refused. The
  conductor never records a verdict for the lead, but that is the conductor's rule and
  not the store's: the store lets any named agent of the session verify a task it did
  not hand back (a peer checker), so the lead holds no verifier binding and no more. Like the binding, the role
  is an environment label, not a credential: a process with a shell can unset it. The store
  therefore refuses verdicts by task-bound non-verifiers and by the executing identity, and
  the conductor distrusts any verdict not recorded by the verifier it assigned or by the
  operator (`verdict_by_unexpected_actor`); a mission does not close on such a task. **The evidence rule:** a task of kind
  `task` that declares files cannot be finished to Review with a result unless it holds a
  commit, diff, test, file or log artifact (a report does not count); the check runs in the
  store, inside the hand-back transaction, for every caller. It is self-asserted: an
  artifact reference is a string the agent wrote and is not validated, and the files a
  task declares come from notes the planner wrote, so the rule makes skipping evidence
  a visible refusal and is not proof that the work exists. When a mission's tasks are all
  done or verified, `mission_finish` only proposes the close (it attaches the final report and
  emits `mission.report`); the session stays running and only the operator closes it
  (`session close`, which refuses an agent). No code path of the conductor closes a session.
  **A mission's confirmation** (the person's "run this send as a mission") is a record,
  not a flag: the coder host's chat children inherit the host's environment and are
  reparented, so they look like the operator to environment and ancestry checks, and
  nothing a turn is given on its command line or in its message can be trusted to mean the
  person confirmed. The record is bound to one queued command (its id and its creation
  time, so a record cannot match a different command that reuses the id), signed under a
  key derived from the profile's vault key (so a row written without that key does not
  verify), lives
  under a reserved config prefix that only the operator writes, deletes or reads, is single
  use, and is revoked when its command is cancelled, expires, fails or is deleted with its
  conversation. A send marked as a mission never runs plain: without a live record it
  fails. The token reaches the child over an inherited pipe, not in its
  environment or arguments: the host writes it to a fresh pipe, closes the write end and
  passes the read end; the child checks that the descriptor is a pipe, reads at most a few
  hundred bytes once, with a deadline, closes it before it starts any process of its own,
  and refuses anything malformed. A value in a process environment can be read by other
  processes of the same user until the process exits; with a pipe the value is exposed only
  for the moment between the host's write and the child's first read, and nothing is left to
  read afterwards. The session token, which stays in the turn's environment, is unchanged. Only the descriptor's number, which is not secret, is in the
  environment. On Windows the same pipe is passed as an inherited handle. The token is also
  single use and is dead once the child has consumed the record, early in the turn. The limit of the MAC: the same user with keychain
  access (the key comes out of the OS keychain, or the file keyring that stands in for
  it, and the database is a file that user owns) can sign a record, so none of this stops
  such a process; the guard is against agent turns going through the CLI, which refuses
  them these commands.
- **A setting that changes what agents may do must be changed by the operator only:**
  the configuration rows that gate approvals, permissions or sandboxing, or that decide
  what runs unattended, are written and deleted only by the operator's own terminal, and
  the single-use ones are read only there as well; `config set` and `config delete` refuse
  anything else with the operator-only exit code. The two newest reserved rows,
  `memory.runs` and `workflow.error_workflow`, are not hidden from reads: any caller may
  see them. Of the writers, only `Database.SaveConfig` (and the config store built on it)
  refuses a reserved row from a caller that is not the operator. The few raw SQL writers
  (the sandbox fallback, browser permissions and site rules, the coder settings and project
  map, `config set`, the coder host's confirmations) are protected only by their own
  commands' operator checks, and a test fails when a file starts touching the configs
  table without being listed and justified, or holds more statements than its entry
  allows. Plain preferences and the selector rows an automation resolves stay open to
  every caller. A workflow's own `error_workflow` setting is part of its definition, so
  saving or editing it by anyone but the operator leaves the definition unapproved; an
  unapproved definition's own setting is ignored (only the reserved default applies), so an
  agent-written workflow cannot start an approved error workflow as the operator's run. The
  check reads marks, not identity: a process that deliberately hides its marks, or edits
  the database file, is not stopped, as with the other operator-only commands.
- **The operator-only commands refuse an agent-driven caller:** `board`
  (it shows the Inbox) and `approve` (both unless delegated, see above),
  `edit`, `move`, `archive`, `unarchive`, `add --ready`, `agents allow`,
  `agents deny`, `session approve`, `session resume`, `session close`,
  `session archive`, `session budget` and `undepend`. A caller is agent-driven when an
  agent-context environment variable is set (the markers org signing
  already uses, `CLAUDECODE` among them), or `--as` is given, or
  `MONOAGENT_ACTOR` is set. This stops an agent acting by accident or on
  injected text; it does not stop one that deliberately unsets its
  environment, as with org signing. Its cost: nothing can be approved
  from inside an agent's own shell, so the user approves in a terminal
  of their own.
- **Limits that stop a loop from flooding the board:** 2,000 open
  tasks per profile (every
  task that is not archived, Done ones included; a task that comes back
  from the archive counts), and caps on the size of titles (200
  characters), notes (64 KiB) and comments (8 KiB) and on a task's
  history (a task with 500 events takes no more comments, one with 2,000
  events no more claims).
- **Text is cleaned on the way in:** invalid UTF-8 is replaced, control
  characters (a terminal escape sequence cannot reach the user's
  terminal) and hidden characters (Unicode tag characters, bidi
  overrides, embeddings and isolates, the byte order mark) are removed.
  Only http and https links are kept, with their user-info removed; a
  link that holds a control or hidden character is dropped, not
  rewritten. Other invisible characters (zero-width spaces, variation
  selectors, the left-to-right and right-to-left marks) are not
  removed: treat task text as untrusted whatever it looks like. The CLI
  prints a task's notes indented, between a notice that they are
  untrusted and a closing line.
- **The macOS menu** (`task os install` and `task os uninstall`, the operator's) hands the selected text to `monoagentcli` on standard input, never on a command line, and files it as a capture: Inbox only. It passes no `--as` and keeps the environment it runs in, so under an agent-context variable the CLI refuses `--source os`; as with the operator guard, this does not stop an agent that deliberately clears its environment or drives the Services menu through the screen, and the human gate still holds. It writes only a bundle it marks as its own, never replaces another profile's menu, and replaces a bundle of the same name it did not write only with `--force`.
- **Claims are cooperative.** The name given with `--as` is a label, not
  a credential: two agents that choose the same name are one claimant.
  The labels `you`, `agent`, `capture`, `chrome` and `os` are reserved,
  in any case, so an agent's events never read as the operator's or a
  capture's.
- **Tasks from the Chrome extension.** The extension adds tasks through the bridge it already uses (loopback, paired token): one request method, `task.add`, which only adds to the Inbox of a profile that exists, as the browser capture `chrome`, whatever the environment of the process that hosts the bridge; a task the side-panel chat proposed and the person added with its card's button (kind `chat`) is filed as the agent `agent:extension-chat` instead, still Inbox only. The page address sent with a task loses its user-info, fragment and session-token parameters in the extension, and MonoAgent checks it again; a tab title that is only that address is not sent either. A selection carries what `getSelection()` reads: text hidden with `display:none` is left out, text hidden by colour, size or position is not. Both are why the operator reads a captured task before approving it. The floating selection panel that carries the task button lives in a closed shadow root and ignores events with `isTrusted` false, so a page cannot find or press it; a page can still move or cover its host and bait a real click, which files an Inbox task with text the page chose.
- **No HTTP route and no new port.** The task board does not listen on the network.

## Persistent coder sessions (coder host)

A coder conversation can be carried on by the **coder host**, a detached process per database
(`coder host`, `coder sessions`; AGENTS.md, "Persistent coder sessions"). Since this feature a coder session, a
mission and a master's child sessions run **unattended**: nobody has to be at the app, and a turn is started by
the host. This section lists what that adds and what bounds it. It is a same-user design: it keeps a model, an
injected text and a confused script inside the rules; it is not a sandbox against a process of the same user.

- **Unattended is visible and is not a grant.** The pane shows `Unattended: on · <access>` for every session. A
  session runs at the access level of its conversation and the host never grants or raises access: starting a
  `full` conversation keeps its own operator acknowledgement, a child is at most its master's access capped at
  `workspace` (never `full` by spawn; an operator raises it), and the `coder set` caps still apply. The send gate
  (human approval of an agent's sends), access levels, budgets and the operator-only settings are enforced in
  code by the host and the stores, not by a prompt; no preset of the autonomy policy changes them.
- **Who may steer a session.** `sessions send|stop|pause|resume`, `settings set`, `questions answer`,
  `decisions revert`, `host run|stop` and mission confirmation are operator-only: they refuse an agent-driven
  caller (the same guard as the task board, with the same limit: a process that scrubs its own environment is
  treated as the operator). `sessions list`, `settings get` and `command` need a session token (they then show
  the caller's own tree only) or the operator.
- **Session tokens.** The host mints a token for each turn (`MONOAGENT_SESSION_TOKEN`). It names profile,
  conversation and turn, is valid only while that turn is the session's current one (24 hours at most), is stored
  as a hash and signed under a key derived from the profile's vault key, and counts only from the turn's process or
  a descendant of it (Linux; elsewhere the signature and the turn only). With a token the master verbs
  (`sessions send|stop|pause|resume|spawn|status`, `settings set`, the MCP tools `session_*`) act **only on
  descendants**, checked in the database; a peer, an ancestor, the session itself or an unrelated session is
  refused, and a bad token is a refusal, never a fall-back to the operator path. A token can change only the
  settings on the `AgentAdjustable` allowlist; limits, autonomy, `mesh_accept`, role, access, unattended and the
  budget stay the operator's. A tool that daemonises or runs in another pid namespace is outside the tree and is
  refused. The limit: a process of the same user can read the database and the keychain and mint what it likes.
- **Mission confirmation is never made by the host.** A mission starts only from a record the operator wrote
  (see Task board, "A mission's confirmation"). A turn the host makes by itself (`checkin-`, `answer-`, a
  master's `master-` and any later prefix) can carry no mission mark: the store refuses a mission send with a
  host-made turn id or with an issuer, and a master's message is tagged with the master as issuer. A check-in, a
  parked-question answer, a default taken after its timeout and a mission progress line in a digest cannot
  approve a plan, close a task, resume a session or confirm a mission: the digest holds only the host's own
  records (ids, state words, counts), never a session's title, goal or a task's text.
- **Budget caps.** A child's `--budget-usd` is carved out of its master's cap; a capped master must give a
  positive amount that fits what is left (budget less spent less carved). Each turn of a capped session runs under
  what it has left as a ceiling for that turn. A runtime that cannot enforce a cap (kilo) is refused for a capped
  session. Raising any budget is operator-only. Documented slack: the running turn's own cap is not reserved, so a
  capped master's turn can spend its cap and carve to its children in the same turn (up to about twice the
  budget per master turn; chains do not compound).
- **Spawn and run limits.** Depth 2, `max_children` per master, 6 spawns per 10 minutes
  per master, 5 pending slots per child shared by everything a master queues except a stop (sends, pause,
  resume) and 2 master stops apart, queued sends that expire (6 hours; a master's sends, not its pause or resume,
  after 15 minutes), 50 waiting sends and 50 waiting control commands per conversation. `max_concurrent` (default 2) caps the children of one master that run a turn at once (only turns this host started are counted); further sends wait (in order within a chat; across chats the freed slot goes to the master's oldest waiting send), and a held send can expire like any other (15 minutes for a master's, 6 hours for others including `answer-d-` notes) and then fails with the expiry reason.
- **Check-ins and the streak.** After `checkin_limit` (default 48) host-made turns (`checkin-`, `answer-`,
  `master-`) in a row with no message from a person, a session waits for one. A new path that starts turns
  without a person must use its own prefix and be counted, or it would reset the limit.
- **Autonomy decides who answers a gap, nothing more.** `risky_action` and `missing_credential` are never
  answered by the model and never take a default. A model-made decision is written to the decisions ledger as the
  model's. The producer tools (`session_park`, `session_record`, `sessions park|record`) take the caller from the
  verified session token (no session argument), validate the class, refuse a hard class as reversible or as a
  recorded model decision, and consult the policy: a park the policy leaves to the model and a record the policy
  leaves to a master or the user are refused. They start no turn and carry no approval. The `reversible` flag is the
  model's claim and is shown as such in the ledger. A model's ledger rows rotate only among its own and `record` is limited to 20 an hour per session. A master answers
  its direct child's question under its own token (`session_answer`); every question that is not its direct child's
  (including one of a deeper descendant) is refused with one text. Reading questions and decisions needs the operator
  or a session token, is limited to the caller's subtree, and returns model-written text cleaned.
- **Mesh text is data.** What other agents send to a session over monomind's mesh is sealed as untrusted peer
  content in front of its next turn; it starts no turn and carries no authority. The mesh name is not a trust
  anchor and `mesh_accept` (`none`, `subtree` or `project`; default `subtree`) limits who is heard.
- **Not covered.** Anything a same-user process can do (read the database, the keychain, `/proc/<pid>/environ`
  of a turn until it exits). Mission leads and task sessions run in the host but the host does not yet start a
  child session per task: a task's work is done by the conductor's workers (AGENTS.md, "Persistent coder
  sessions").

## OpenAI-compatible API surface

`monoagentcli httpapi` and `monoagentcli daemon` serve `GET /v1/models`,
`GET /v1/models/{id}`, `POST /v1/chat/completions` and
`POST /v1/images/generations` over the agent
runtimes installed on the machine (`internal/openaiapi/`). A request is a
real agent turn run through `monomind agent exec`; this section is what that
means for exposure. Setup: `examples/openai-api-quickstart.md`.

**Credentials.** A key is `sk-ma-` plus 32 random bytes and belongs to one
profile. Only its SHA-256 is stored (table `api_keys`), so it is shown once,
at creation, and verifying a request needs no vault and no keyring. There is
no authentication cache: `api key revoke` and `org teardown-profile` take
effect on the next request. A request authenticates as the key's profile and
as nothing else; a key of another profile is "not found" in every command
except `api key list --all-profiles`, which lists every profile's keys
(metadata only, never a secret) to whoever can run it on this machine.
`api_key_list` (MCP) lists the MCP server's own profile's keys, metadata
only. No MCP tool creates, changes, revokes or returns a key: `api_key_create`,
`api_key_update` and `api_key_revoke` refuse every call, since an MCP caller is
an AI. `api key create|update|revoke` and `org teardown-profile` are your own:
an agent, the chat or a workflow is refused (exit 3, `operator_only`). Run
`api key create` in your own terminal, not through an agent's shell tool: it
writes the key to its stdout (also with `--json`). A key is refused as the name of a key (names are listed, and one
holding `sk-ma-` or shaped like a key id is refused), and no error of these
tools repeats an argument.
The legacy HTTP API token (vault entry `httpapi-token`) is a separate
credential for the other routes: a key never opens them and the token never
opens `/v1`. Treat a key like a password; it has no scopes and no expiry.

**What a key can make this machine do.** Every request starts one agent turn
in a slot folder of its profile, `~/.monoagent/workspaces/api/p-<hash>/slot-N`
(`<hash>` is a hash of the profile id, so two profiles never share a folder;
never the profile's own folder; emptied before and after every turn), with no
tools (the functions a request declares aside: see **Tool calling**), no
settings and the workspace-write sandbox where the runtime has
one. What that confines depends on the runtime, and
`monoagentcli api models` reports it per model instead of pretending
otherwise:

| Class | Meaning | For example |
|---|---|---|
| `chat-only` | monomind's allow-list gate is the only tool gate, and a request carries no caller tools, so no native tool is reachable | claude |
| `sandboxed` | writes are confined to the turn's folder and the system temp directory; reads and the network are open, and the runtime's own configuration still applies | codex |
| `unconfined` | the runtime's native tools run as the OS user | antigravity, and any runtime monomind does not vouch for |

Which runtime lands in which class follows what monomind reports, so it can
change with a monomind upgrade. `sandboxed` confines writes, nothing more: a
runtime keeps the MCP servers and instructions files of the OS user it runs
as, so a key holder can ask codex to use any tool you configured for it.

Profiles are kept apart by their folders and by what the API looks up for a
key, not by an OS boundary. Every profile's data lives under the same
`~/.monoagent`, and a runtime that can read the disk (codex under `sandboxed`,
antigravity) can read it, the prompt files of a turn that is running for
another profile included.

The class is decided from `monomind agent scan` and fails closed: anything
not vouched for is `unconfined`. A `sandboxed` model's turn is started with
the sandbox required, so one whose sandbox cannot be applied is refused (403)
instead of running unconfined. The class is checked again when the turn
starts, against the confinement the `start` event reports (monomind 2.22
reports it), and a turn that is weaker than the listener's policy is
cancelled (403 `policy_denied`) before any of its text reaches the caller. A
streamed response is committed (status 200) on its first content or after 5
seconds of silence, so a refusal later than that is an error event in the
stream rather than a 403. A successful non-streaming response also carries
monomind's sandbox verdict for the turn in `X-Monoagent-Sandbox` (`sandboxed`,
`scoped`, `unsupported`, `awaiting-monomind`, `needs-monomind` or `off`),
which is not the class above; a streamed response and an error have none.

Be precise about what is and is not guaranteed. claude's `chat-only` rests on
monomind's design (its allow-list gate); the live check
`MONOAGENT_LIVE_API_TESTS=1 go test ./internal/openaiapi -run TestLiveClaudeIsChatOnly`
tries to make it do otherwise: it asks claude to run `ls /`, read `/etc/hosts`
and write a file. It was run on 2026-10-02 against claude 2.1.287 and monomind
2.22.0: claude did try its Bash, Read and Write tools, monomind refused every
call ("not in the tool list this exec call was given"), nothing ran and no file
was written. That is one machine and one version of each, so run it again after
upgrading either; read the class as measured for that pair and as the design
beyond it. codex under
`workspace-write` can still read files outside its folder, and antigravity
can run a shell command with the permissions of the OS user. **Run the server
as a dedicated unprivileged OS user**, with nothing of value readable by it,
before giving a key to anyone you would not give a shell.

**Exposure.**

- `/v1` rides the main HTTP API listener only while that bind is loopback
  (default `127.0.0.1:9322`), where by default every runtime is served.
  Off-loopback the
  main listener never serves `/v1`; it logs a pointer to `--v1-addr`.
- `--v1-addr` (`MONOAGENT_API_V1_ADDR`) starts a dedicated listener that
  serves only `/v1/*` and `GET /health`: no workflow, node, HIL or org
  endpoint exists on it. Any non-loopback bind is served only over TLS, with
  no way to serve it in the clear: `MONOAGENT_API_TLS_CERT` and `_KEY`, else
  a self-signed certificate cached under `~/.monoagent/api-tls/` (key file
  mode 0600) that covers `localhost` only, so remote clients reject it until
  they trust it explicitly. Set a real certificate, or terminate TLS in a
  reverse proxy. The two variables also make a loopback `--v1-addr` bind
  speak TLS, so a proxy that forwards plain HTTP needs them unset. The webhook
  server follows the same rules (see above). `httpapi` exits when the
  listener cannot start; `daemon` prints a warning and keeps running without
  it, so check `api status` after starting it.
- The default confinement is `chat-only` off-loopback and `any` on loopback.
  Behind a reverse proxy the bind is loopback, so **set `--confinement`
  (`MONOAGENT_API_CONFINEMENT`) explicitly**; it is one value for every
  listener of the process, so it also limits the loopback main listener. A
  model above the policy is not listed, `GET /v1/models/{id}` answers 404 for
  it and a completion that names it answers 403.
- TLS protects the key in transit; it does not limit who may try one. There
  is no rate limit per caller and no lockout, so a key is only as safe as it
  is long and secret (256 bits). Put a proxy or a firewall in front of a
  listener that faces the internet. There is no CORS: browser clients are out
  of scope.

**Saved server settings.** The settings that decide the exposure above
(`--v1-addr`, the TLS files, the three confinement classes and the two runtime
lists) and the two limits can be saved with `monoagentcli api config set`, so
that a daemon the login service starts, which has no flags, has them. They are
one JSON row (`api_gateway_config`) of the `settings` table of
`~/.monoagent/monoagent.db`: paths of TLS files, never their contents, and no
secret. The order is flag, then environment variable, then saved, then default,
so a saved value never loosens what a flag or a variable of the process says (a
`MONOAGENT_API_CONFINEMENT=chat-only` in the service's own environment holds
whatever is saved), and it fills what they leave out.

- Whoever can write that database (the OS user the daemon runs as, and root)
  can change what the daemon exposes the next time it starts, as whoever can
  edit its LaunchAgent, systemd unit or Scheduled Task can. The row is not a new
  boundary: run the server as a dedicated OS user, whose database it is.
- A change takes effect when the server starts, never while it runs.
  `monoagentcli daemon restart` starts it again through the service manager the
  daemon is registered with, and **interrupts what the daemon is running**
  (workflows, org runs); it says so first. A daemon that is not registered, or
  that was started by hand, is not restarted by it. How the daemon ends is the
  service manager's (launchd, systemd) and nothing here claims a graceful stop.
  On Windows the Scheduled Task is ended with `schtasks /end`, and whether that
  stops the process its `cmd` wrapper started was not verified on Windows; the
  command waits for the daemon to release its lock before it starts it again.
- A change that makes the server reach further needs `--yes` (`api config set`
  and `unset`; without it exit 3 with the reasons, `--dry-run` shows them): a
  dedicated listener that reaches further than the saved one (beyond this
  machine, another host beyond it, or every interface where it was one host), a
  higher confinement class (of a listener, of a `--context` key or of `auto`) on
  a listener on this machine or beyond it, a runtime list that gains a runtime
  it did not have (one of the default list that a saved list left out counts
  when it comes back, so undoing a `codex`-only list needs `--yes`), tool calling
  or image generation switched on again, or the removal of a saved
  row that cannot be read (below). It guards against a
  script, an agent's shell tool or a hurried edit widening the server without
  saying so. It is not an access control: whoever can run the CLI as your OS
  user can pass `--yes`. The check compares the effective policy, so it also
  catches an `unset` that takes a value held below its default back up (a
  class, or a runtime list). One
  cost: raising `confinement` always needs `--yes`, because the daemon's own
  environment may name a listener beyond the machine that the row does not.
- What the saved text may be. The saved layer outlives the process that wrote
  it, is printed by `api config show` and handed to every model that reads the
  settings, and can be written by a caller that is not the operator, so it is
  held to two rules the flags and the environment are not: no value may contain
  a control character (an escape sequence in a TLS path would drive the
  terminal that shows it; `show` also writes any that is already in the row
  out as `\x1b`), and a TLS file must be an absolute path (a service starts in
  another folder and nothing expands a `~`). A certificate or key file is read
  as a regular file of at most 1 MiB, whoever named it, so a path that names a
  FIFO or a device such as `/dev/zero` is an error that the server reports and
  not a start that hangs or runs out of memory. Printable text is still text:
  a model that reads a saved path reads whatever words it holds, which is a
  reason to give an agent host that can change settings no more than it needs.
- The desktop app (Settings › OpenAI-compatible API › Server settings) can
  change these settings through the same commands, and before a change that makes
  the server reach further it lists the reasons the CLI gives in a dialog whose
  default is Cancel (it passes `--yes` only after that confirmation), as it asks
  before it restarts the daemon.
- A saved value that fails its rule (a hand edit) makes `httpapi` exit at
  start (exit 3, naming the setting), and `api models` and `api status`
  refuse, rather than guess; `api config unset <setting>` removes it. A row that
  cannot be read at all (not a JSON object, a bad version, a field of the wrong
  type) stops every `api config` command, `httpapi`, `api models` and
  `api status` too, with exit 3 and a message that starts `the saved settings
  are damaged`; one command removes it: `api config unset --all --yes`. The
  daemon does not stop with either: it does more than serve the API, and a
  login service would start it again and again, so it starts without the
  OpenAI-compatible API (no `/v1` anywhere, nothing in its heartbeat), says why
  on stderr and in its log, and runs everything else; it never ignores a saved
  setting to start the API, which would serve with the defaults a server that
  someone had limited. `daemon restart` refuses, restarting nothing, when the
  saved settings cannot be used. Removing a row that cannot be read asks
  for `--yes` because what the row limited cannot be told (a `confinement` of
  `chat-only` in it, say), so returning every setting to its default may reach
  further than anything: the same gate as any other widening, so a damaged row
  is not a way to drop a restriction without confirming. What the row held is
  gone, and the server then starts on its flags, its environment and the
  defaults. A row written by a newer `monoagentcli` is the exception: it is exit
  1, never rewritten and never removed by these commands, since that would lose
  what that version saved.
- The daemon's heartbeat (`~/.monoagent/daemon-heartbeat.json`) lists each
  setting's effective value and where it came from (`flag`, `env`, `saved`,
  `default`): addresses, classes and paths, no secret.
- **From MCP.** With `mcp --allow-mutations`, `api_config_set` saves and removes
  the same settings, with the same checks and the same gate, and the
  confirmation moves to where a model cannot reach it. A change that makes the
  server reach further (the cases above, `unset` included) is refused, and
  nothing is saved, unless the operator started the MCP server with
  `--allow-api-exposure` (or `MONOAGENT_MCP_ALLOW_API_EXPOSURE=1`), a switch read
  once when the server starts. It is a flag of the host's configuration and not
  an argument of the tool because a model sets the arguments of a call: an
  argument that confirmed would be set by whatever the model was told, a
  prompt-injected page included, and would protect nothing. No argument can open
  it (the schema offers none, and the tests send the obvious spellings of one), and
  the refusal names the setting and the reason, the operator's switch, and the
  user's own way: `api config set ... --yes`, or the desktop app (for a row that
  cannot be read it names `api config unset --all --yes` alone). Removing such a
  row (`unset` of `all`) is one of the changes the flag allows, since what the
  row limited cannot be told: without it the call is refused with the reason
  `saved_settings`, and with it the row is removed and the result says
  `removed_unreadable_row`. While the row cannot be read the other changes and
  the read tools fail with the command's message (`the saved settings are
  damaged ...`), which a model passes on to the user, and a row a newer version
  saved is an error that nothing here removes. **What it does
  not do:** it does not stop a host that also gives the model a shell tool, or
  any tool that runs commands as your OS user, from running `monoagentcli api
  config set ... --yes` itself; the flag guards the tool as `--yes` guards the
  command, and neither is an access control. **`--allow-mutations`, which the
  tool needs first, is itself such a tool:** it also serves `workflow_node_add`
  (which accepts the node type `system.execute_command`), `workflow_set_active`
  and `workflow_run`, and a model that can use them can have a workflow of the
  profile run `monoagentcli api config set ... --yes` as the OS user (the
  security review of phase 6 did it, against a workflow that had a trigger). The
  flag therefore stops `api_config_set` and `api_auto_set` and nothing else that
  `--allow-mutations` allows. If the model must not be able to widen the server,
  do not give it `--allow-mutations`, start the MCP server with `--api-only`
  (below), or run it in a sandbox.
  `--allow-api-exposure` without `--allow-mutations` does nothing, since the
  tool is then not served, and `mcp --grant` refuses it. The tool also refuses a
  value that holds an API key (`sk-ma-`) and a value of unreasonable length, so
  that a key pasted by mistake is not saved in clear where the read-only
  `api_config_get` shows it to whoever calls it; no error of the tool repeats an
  argument (the reasons of the gate print the address of the call and, for a move
  between two binds, the one saved before it, or a runtime; the refusal names no
  address and no host, in any spelling, and no runtime outside the default list);
  and the result of an allowed change shows what was saved, paths and addresses
  included, and the command's reasons, never a secret.
- `api_config_apply` (`--allow-mutations`) restarts the daemon through the
  service manager it is registered with, as `daemon restart` does, **and
  interrupts what the daemon is running** (workflows, org runs), with whatever
  is saved at that moment, which includes a change that reaches further that the
  user saved with `--yes` and did not mean to apply yet. The tool is annotated
  destructive so that a host can ask first, and its description says so. It
  restarts only a daemon registered for auto-start (anything else is an error
  that says what the user can do), and nothing here claims a graceful stop.
- `api_auto_set` (`--allow-mutations`) switches `auto` on or off for the MCP
  server's profile, as `jev enable api_auto` does. Switching it on makes the
  server send the first 4,000 characters of each `auto` request's prompt, and
  the models' descriptions, to TypeSafe (see **The `auto` model**), so it needs
  the operator's `--allow-api-exposure` too (the owner's decision of 2026-10-05:
  what leaves the machine is the operator's to allow, made when the server
  starts, as the exposure of the API's server is, and no argument of the tool
  can make it) and `acknowledge_egress: true`, which shows that list, as `jev
  enable` needs `--yes`. The acknowledgement is an argument, so like `--yes` it
  makes the data visible before it moves and is not an access control; the flag
  is. Switching off sends nothing and needs neither. Only where a Jev key
  already exists does switching on send anything. The tool never creates,
  stores, uses or shows the Jev key; it only asks where one is.
- `--api-only` (or `MONOAGENT_MCP_API_ONLY=1`) gives a model the API's tools and
  no other: the ten `api_*` tools, with no workflow, vault, secret, person, org
  or documentation tool, so with `--allow-mutations` it has nothing that runs a
  command as the OS user, which `--allow-mutations` alone does (above). It
  takes tools away and changes none that stay: the mutating ones still need
  `--allow-mutations`, and `--allow-api-exposure` is still what lets `api_config_set`
  widen the server and `api_auto_set` switch `auto` on, so the flag is a boundary
  for what that model can reach through this server. It is not one against a host
  that gives the model tools of its own besides this server, such as a shell
  tool. A call by name of a tool the server does not serve is refused and says
  why; `mcp --grant` refuses the flag.

**Context keys.** A key created with `--context` adds up to five excerpts
(1,200 characters each, source base names only, never paths) from that
profile's own documents and captures to the system prompt, framed as data
whose instructions must be ignored. Captured web pages are in that knowledge
and nobody vetted them, and the framing reduces prompt injection without
removing it, so **by default a context key is served only by `chat-only`
models**: a runtime with native tools could be steered into using them.
`--context-confinement sandboxed|any` (`MONOAGENT_API_CONTEXT_CONFINEMENT`)
raises that on purpose, for example to give a coding agent on your own
machine your notes. It never goes above the listener's own `--confinement`,
and raising it accepts that a captured page could steer that runtime. The same
cap decides **tool calling**: a request that declares `tools` (and does not set
`tool_choice` to `none`) with a context key is 403 `policy_denied`, before
anything starts (naming `--context-confinement`, or `--confinement` when the
listener is chat-only, which holds the key to the lower of the two, or both),
unless that cap is above chat-only, because an
instruction in a captured page could steer the calls the model proposes and the
client runs those with its own authority. The
excerpts leave the machine like any prompt, to the runtime's provider. The
personal brain and other profiles are never searched.

**The `auto` model.** A request for `"model": "auto"` has TypeSafe Jev pick
the runtime and model. It is off until the key's profile runs `monoagentcli
jev enable api_auto` (which prints what follows and asks first) and has a Jev
key: its vault entry, else the server's `TYPESAFE_API_KEY`, so with that
variable set every profile that switches the surface on spends it. Then each
such request sends TypeSafe, a third party besides the runtime's provider that
answers the prompt, the first 4,000 characters of the last user message (of an
image request, its prompt) and,
for every model the key may use, its name, description and validated cost and
latency. Never the system prompt, earlier turns, the excerpts of a context key
or a key. So whoever holds a key of that profile decides what is sent to
TypeSafe: enable the surface only where you accept that. `auto` is listed after
the concrete models, so a client that takes the first model of the list is not
moved to it, and its prompts to TypeSafe, by switching the surface on.

What Jev can decide is bounded by structure, not by its instructions. The prompt
goes as data (`state.untrusted_prompt`, and Jev is told to treat it so), but Jev
can only answer with one of the listed model ids, anything else is discarded,
and the list is already cut before Jev sees it: to what the key's policy allows
(the listener's `--confinement`, a context key's `--context-confinement`) and
within `--auto-confinement`, which is **chat-only unless the operator raised
it**. A prompt that steers Jev can therefore never reach a model the key could
not have named itself and, by default, never one with native tools. The author
of a prompt need not be the holder of the key (an app that forwards its users'
text, say), so letting a prompt choose the confinement class of its own turn is
a permission the operator gives, with `--auto-confinement sandboxed|any`
(`MONOAGENT_API_AUTO_CONFINEMENT`); `api models` shows which models auto may
pick and how many are held back. A model the client names itself is not subject
to it. A context key stays at chat-only unless `--context-confinement` says
otherwise, through `auto` as well.
Jev's answer is used only when it arrives within 8 seconds (looking up the key
included), with no retry, and is sure enough for the surface's threshold; an
outage, a timeout or a doubt falls back to a rule, never to a wider set: of the
validated models the most confined, then the cheapest, then the fastest, so that
an outage does not move a request to a less confined model for being cheaper. An
outage costs a profile's first three `auto` requests up to those 8 seconds each,
with their slots held; then its questions stop for 30 seconds and the rule decides
at once, and one question after that finds out whether Jev is back.
Each question is recorded in `jev_usage` under `api_auto` (counts only), and the
log line of the request says `auto=jev` or `auto=rule` and never the prompt. The
gateway blanks `TYPESAFE_API_KEY` in the environment of its agent turns, which
inherit the rest of the server's: a turn that runs commands could read it.

**Image generation.** `POST /v1/images/generations` runs one turn of a runtime
from `MONOAGENT_API_IMAGE_RUNTIMES` (codex and antigravity unless the operator
changed it) that runs as `sandboxed` or `unconfined`: the runtime has to write
the file, so a `chat-only` runtime cannot make images, and the route is refused
under `--confinement chat-only` and, for a key created with `--context`, under
`--context-confinement` (chat-only unless raised). An operator who wants no
image route at all sets `MONOAGENT_API_IMAGE_RUNTIMES=none`: nothing runs, every
image request says it is switched off, and no model is listed with the `image`
capability. It is the chat turn in the same slot folder, so everything above
holds. Two things differ, and both reach further than chat does.

- *The turn is told to use the runtime's native tools.* It makes the image with
  its own tool and then copies the file into the folder it was given with a shell
  command from the runtime's own state folder (in the probes of 2026-10-01,
  codex's `~/.codex/generated_images/` and antigravity's
  `~/.gemini/antigravity-cli/brain/`), and a runtime may read its own skill files
  outside the folder first. What a `sandboxed` or `unconfined` runtime can read
  or run was already in reach of such a key through chat; here it is the normal
  path.
- *The gateway returns bytes.* Each turn is told a folder of its own to save its
  images in, `./out-<16 random characters>/` inside the slot folder, which the
  gateway makes (mode 0700) before the turn starts. After the turn, and before the
  slot folder is emptied, it reads the files at the top of that folder, and of
  nothing else, that are PNG, JPEG, WebP or GIF by their first bytes (at most
  20 MiB each, at most `n` of them, `n` at most 4) and sends them in the response.
  A key holder, or a prompt that steers the runtime, can therefore have it copy
  any image file it can read into that folder and receive it, and the test is on
  the first bytes only: a file that begins with the eight bytes of the PNG
  signature and goes on with anything is returned, so a runtime that can read a
  file can have that file returned by putting those bytes in front of it. Under
  `sandboxed` and `unconfined` the runtime reads the disk as the OS user, and chat
  could already have it say what it read; what this adds is the transport of the
  bytes themselves, of any file it can read. The gateway runs as that user outside
  the runtime's sandbox, so the collection does not trust the folder. It never
  follows a link (one planted to an image elsewhere is left out, and the log
  counts it), never opens a FIFO or a device (a read would wait for a writer),
  ignores folders, and reads through the folder's own handle, the one the emptying
  uses, which refuses a path that leaves it; a file that is swapped between the
  look and the read, or grows past 20 MiB, is left out. It is bounded as well: of
  a file that is no image only the first 12 bytes are read, no more than 64
  entries of the folder are looked at (the first 64 the system lists, so "the
  first `n` by name" is among those), and it gives up after a minute or when the
  client has left, and does not wait for a step that does not return. A copy or a
  hard link is a plain file and is returned: a runtime can make one of anything it
  can read, which is the point above.

The folder is new for every turn and its name is not known beforehand, so that a
process an earlier turn left running that keeps writing to the paths it knew (the
slot folder, or the earlier turn's folder) does not put a file into a later
request's response. What it writes elsewhere is not returned: the log line counts
the files at the top of the slot folder that are outside the turn's folder, and a
turn whose runtime ignored the folder it was told is a 502 and not a silent
fallback to unnamed files. That is all the name does. A process of the same OS
user that survives and looks can list the slot folder while a later turn runs,
read the name of the folder, and write into it, or read what it holds (the images
of the turn, before they are returned): the name keeps a writer that does not look
out, it does not keep out one that does. Run the server as a dedicated OS user and
look at what it leaves, as below.

An image turn took 40 to 105 seconds and about 40,000 input tokens in the
probes, on the runtime's own account (codex and antigravity report no cost), and
there is no quota per key: the concurrency cap is the bound. Its prompt goes to
the runtime's provider, and with `auto` the first 4,000 characters of it also to
TypeSafe. No excerpts of a context key's knowledge are added to an image
request. `auto` picks among image models only within `--auto-confinement`, so by
default none: image models are `sandboxed` or `unconfined` and `auto` is held to
chat-only until the operator raises it. The runtimes keep the images they generate
in their own state folders, as they do for any use of them: the gateway empties
the slot folder, not those. The response is written as the images are encoded,
not built in memory first, the slot is given back as soon as the turn is over, and
a client has two minutes to read the body before the connection is cut. Until the
last byte is written the images are held in memory (up to four of 20 MiB), so a
client that starts requests and does not read them holds that for those two
minutes for as many requests as it can start turns for: the concurrency cap
bounds the turns, not the responses that wait to be read.

**Tool calling.** A request that declares `tools` gives the model functions of
the caller's own. The model proposes a call and the caller's program runs it,
wherever that program runs and whatever it is allowed to do: the gateway
executes nothing of the caller's. What this changes, and what it does not:

- *The confinement class does not change.* Declaring tools moves no class, no
  policy and no check of the start event, and a `sandboxed` model's turn still
  requires its sandbox. A tool leg of any model, a chat-only one included,
  requires the sandbox too (below): the one thing tools change for chat-only
  models. A runtime that is not `chat-only` keeps its own tools in
  play, which pulled the model away from the declared ones in the spike (codex
  made 31 of 31 native attempts and edited files itself while the caller got
  nothing), so its leg runs with read access: for codex monomind makes the
  sandbox read-only (the start event says `native_sandbox: read-only`, which is
  still the `sandboxed` class; `sandbox_applied` in it only echoes the sandbox
  the gateway requested, and a write probe in the spike found the environment
  read-only). That is stricter than `workspace-write`: such a leg cannot write
  files. It does not shut anything else out: reads anywhere the
  OS user can read, the runtime's own MCP servers and its instruction files stay
  open (the spike saw codex use one of the user's own MCP servers once, in a run
  with tools declared). A model whose runtime monomind cannot run read-only is
  refused with a 400 instead of run with its own tools in play, and so is any
  runtime outside `MONOAGENT_API_TOOL_RUNTIMES` (claude and codex unless the
  operator changed it; an operator who wants no caller tools reachable on a
  listener at all sets it to `none`). claude's own tools stay denied by monomind
  (it denied all 15 attempts in the spike), and that holds only because every
  tool leg requires monomind's sandbox. Under it monomind lets only the prefixed
  names (`mcp__org__<name>`) of the declared functions through; without it a
  declared function is allow-listed by its bare name too, so a function called
  `Bash`, `Write` or `Read` would open the native tool of that name, for a key
  holder, a tool result or a captured page that can steer the call. So every
  leg (first, resume and replay, every runtime) is started with the sandbox
  required, and `monomind.Exec` refuses it (403 `policy_denied`, nothing run, the
  answer names no function) when the sandbox cannot be applied: the scan or the
  handshake failed, or monomind is one whose claude lists no `workspace-write`
  mode (2.19.0 lists only `full`). A model has the `tools` capability only where
  it can be applied, and a request that declares tools for any other is 400
  `unsupported_parameter` before anything starts.
- *What the model proposes can be steered, and for the client's own functions the
  caller decides.* A tool result is untrusted data. The prompt fences it
  (`<function_result>`): the fence is the defence, a model is told that what is
  inside is data. A second layer defangs the fence's tags and any line of a result
  that would open a turn of the transcript, so a result does not pass for the
  user's or the assistant's words, and it reads the result as a model does, not as
  an ASCII pattern does: every character a renderer may end a line at (CR, VT, FF,
  the information separators, NEL, the line and paragraph separators) ends one,
  every kind of space is a space, case does not matter, a look-alike of an ASCII
  character (full-width, bold, circled or superscript letters, full-width
  brackets, ligatures: NFKD) is that character, a letter with a diacritic is the
  letter (precomposed or written apart: `[üser]` is `[user]`), a Unicode tag character
  (U+E0000 to U+E007F, which renders as nothing and which a model may read as the
  ASCII character it is the twin of: "ASCII smuggling") is that character, and
  whatever renders as nothing (control and format characters, the Hangul and
  braille blanks, combining marks) is not there, so spelling a marker or a tag
  with them gains nothing against a reader of that kind (a fuzz of random
  compositions of them, judged by a reader written the other way round, is in the
  tests: that judge calls none of the code, but it shares with it Unicode's
  tables, `unicode.IsSpace`, NFKD and the model of deleting everything that is not
  a letter, a digit, a space or ASCII, so a wrong belief of that model is a blind
  spot of both; a corpus of code, documentation and logs that must come out
  unchanged grades the other direction). A match is neutralised in place and
  nothing else of a result is changed: line ends of every kind reach the model as
  they were (a file with CRLF line ends is read as it is), and so does everything
  that only looks like a marker or a tag. A role marker is a role word in brackets
  at the start of a line, with spaces allowed around the word, or the header of a
  result, `[tool NAME (ID)]`: `[tool.poetry]`, `[User guide](url)`,
  `[tool for tool in tools]` and `[ user = root ]` are text, while the `[user]`
  header of a gitconfig is indistinguishable from a marker and is defanged with the
  rest. A fence tag spelled as the fence spells it (with an underscore) is one
  wherever it stands and whatever follows it, so `List<function_result>` is
  changed too; the spellings a reader folds into it (camel case, no underscore)
  are one only as a closing tag, where the name ends (`Promise<FunctionResult>`,
  `i < functionResult.length` and `</FunctionResultList>` are text,
  `</functionResult>` is not).
  The arguments of a call that a client sends back, which the transcript renders
  outside the fence, are rendered as compact JSON (with the characters that end a
  line escaped, and a marker or a tag found in it defanged as in a result) or, when
  they are not JSON, defanged as a result is; the name
  of such a call is refused (400) unless it is printable ASCII without
  `[ ] < > & ' "` or a backtick. An id of that kind is shown as it is, and any
  other id (a client may send one of 1 to 128 bytes of anything) is shown to the
  model as `call_1`, `call_2`, ... in order of appearance, never as sent: the
  transcript is the only place an id is shown, and a session is resumed only for
  the id the gateway made, which is such a token. The second layer does not look
  through a letter that only resembles a Latin one and is not one with a mark or
  a case of one (a Cyrillic "е", a Greek omicron, a small capital "ᴜ"), which would
  take a table of confusables: against such a disguise the fence is the defence.
  The words of the
  user are rendered as the client sent them (they are the conversation). The words
  an assistant said before a call are not: a result can steer what a model says,
  and the client sends it back, so in a replay (a conversation with tool history,
  whether or not tools are still offered) they are defanged as a result is, and
  are not fenced (a conversation without tools is rendered as it always was).
  The tags of the fence carry no per-request token. The
  knowledge excerpts of a context key have their own, narrower defence: only
  their `<knowledge` tags are defanged.
  None of that keeps a model from following what a result says: a tool that
  fetched a web page can return instructions, and a key created with `--context`
  puts excerpts of captured pages in the system prompt. Either can steer which
  calls the model proposes next, so a context key is refused tools unless the
  operator raised `--context-confinement` (and, on a chat-only listener,
  `--confinement`: the key is held to the lower of the two) above chat-only (403
  `policy_denied`, before anything starts; a result is still untrusted data on
  any other key). The gateway cannot tell a steered call from an asked one, and it
  returns every call monomind lets through, valid or not: monomind rejects a call
  whose top-level types, string enums or required names do not match what it was
  told (such a call never comes back), and one that does not match the rest of its
  schema is returned too and counted in the log. So a client that runs calls without asking runs whatever
  the model was steered to propose, with its own permissions: give such a client
  keys whose prompts you trust, and keep a person, or a policy of the client's
  own, between a call and its execution.
- *Not every call is the client's.* What the caller decides is its own functions.
  A codex leg may use a tool of one of the user's own MCP servers (the spike saw
  it once): the runtime makes that call itself, the gateway never sees it as a
  call to return, the read-only sandbox of the leg does not cover it (it limits
  what the runtime writes, not which servers it talks to) and the caller does not
  decide it, so a steered model can reach it without the client. A leg in which
  such a tool ran is never replayed or given back (so it does not run twice), but
  it ran once. An operator who does not want that removes those servers from
  codex's configuration, or leaves codex out of `MONOAGENT_API_TOOL_RUNTIMES`
  (claude's own tools are denied by monomind, which is stricter).
- *What is kept.* No process waits for a result and no slot is held while the
  client runs the call. A continuation record (the id the client was given, key
  id, profile, model, function name, a hash of the declared tools, a hash of the
  conversation the leg was given, a hash of the call's arguments and the
  runtime's session id) lives in memory
  for ten minutes, at most 1,024 in all and 64 per key, and is used once (given
  back, with the expiry it had, only when a resume fails before the model ran, for
  a rate limit, the quota or a sign-in, and no tool of the runtime's own had
  run: such a tool is never run a second time by a replay or a retry, and a
  retry does not extend how long a session lives); a restart loses them and the follow-ups then start from their
  transcripts. It holds no argument and no result, and a follow-up continues the
  session only when its conversation before the call hashes the same. A record belongs to
  its key: another key, even of the same profile, finds nothing and is served by
  replaying the transcript it sends, and a revoked key cannot authenticate at all.
  The session id comes from the record, never from the client. The agent CLIs
  keep the sessions they resumed or cancelled in their own stores, which hold the
  arguments and results of every leg, as they hold prompts (see below).
- *Logs and errors.* The line of a request with tools adds how many tools it
  declared and how its leg started (`tools=<n> leg=first|resume|replay`, and
  `badargs=1` when a call did not match its schema). It never holds a function
  name, an argument or a result, and an error sent to the caller names a
  parameter, never what the client put in it, and never echoes a tool result.
- *A cancelled leg's runtime can outlive it a few seconds.* A leg ends by
  cancelling its turn: monomind's cancel for claude, SIGTERM to the process group
  for codex, and a group kill only if monomind has not exited within its grace.
  The slot's folder is emptied and the slot freed when Exec returns, not when the
  runtime's process is gone, and in the live check claude's own binary outlived
  a cancelled leg by about 5 s, an orphan of monomind that exited by itself (codex
  left nothing). For those seconds a process of the cancelled leg still has the
  slot's folder as its working directory. A freed slot joins the back of the
  queue, so with the default four another request takes that folder only after
  three others have been served, and `--max-concurrent 1` makes it the next one;
  a turn starts in an emptied folder, and the process wrote nothing there in the
  live check. It is the same runtime under the same confinement, so no policy is
  crossed; a runtime that hung would not be killed, which is a matter for
  `monomind.Exec`.
- *Reliability, not security.* A resumed leg can ask for the same call again
  instead of using the result: 1 of 19 single-result claude legs did in the
  spike, 0 of 18 on codex, and nothing detects it. codex repeats an identical
  call two or three times within a leg (7 of 16 legs): the leg ends at the first
  and ignores the repeats. A client whose tool has a side effect (a write, a
  send) should make it idempotent, or look at a call identical to the last it
  ran before running it again. Neither crosses a boundary; both are why side
  effects the client cannot take back deserve a look first.
- *Cost.* Every leg is a real turn, and a loop of N calls is N + 1 of them, each
  on the runtime's account; a leg that cannot continue a session pays for the
  whole transcript again (a resumed leg cost about half of a replayed one on
  claude in the spike). There is no limit on the rounds of a client's loop and no
  quota per key: the concurrency cap, the body size and the turn timeout are the
  bounds, as for chat. A response carries one call, so a model that wants several
  asks for them in successive rounds.

**Cost and abuse limits.** There are no per-key quotas: every request is a real
model turn on your subscription or account, and some runtimes report no cost
(for what an image turn took, see above).
The bound is the concurrency cap (4 turns, 429 beyond it; `--max-concurrent`),
the 2 MiB request body (64 KiB for an image request) and the 10 minute turn
timeout. A request that is
rejected (invalid, over policy, or busy) starts nothing and takes no slot.
Tools have bounds of their own, each a 400 `invalid_value` that names the
parameter and never what was written, answered before anything starts. Whatever the `tool_choice` (the tools are checked when they are declared): more
than 128 functions; a tool that is not an object; a name that is not 1 to 64
characters of `[A-Za-z0-9_-]`, one declared twice, or an alias that is another
function's name (a name of 55 or more reaches the model as an alias of 54, and
you always see your own); a `description` of more than 16 KiB; `parameters` of
more than 64 KiB, that are not a JSON schema object, whose root `type` is not
`object`, whose `properties` is not an object or holds a property that is not a
schema, or whose `required` is not a list of strings; a `tool_choice` that is
not `none`, `auto`, `required` or a function, or that names a function that is
not declared; and in the conversation more than 64 calls in one message, a call
with an id of no characters or of more than 128 bytes, a call whose name is not
printable ASCII without `[ ] < > & ' "` or a backtick, a call whose `arguments`
is not a string, a result of more than 256 KiB, and a tool message that answers
no call of an earlier assistant message. Other codes: 400
`unsupported_parameter` for a tool or a call of a type other than `function`, a
`tool_choice` of another type, and a `tool_choice` that forces a call when no
tools are declared; 400 `missing_required_parameter` for a tool with no
`function`, a `tool_choice` that names no function, and a tool message with no
`tool_call_id`. While the tools are passed to the model
(`tool_choice` is not `none`, which passes none, so no argument is named and these
do not apply): a function whose schema names no property and allows free-form keys
(`additionalProperties` or `unevaluatedProperties` true or a schema,
`patternProperties`) or whose references cannot be followed (`$dynamicRef`, a
`$ref` that is not local or leads nowhere); a schema whose `const` or `enum` (the
root's, an `allOf`'s or a `$ref`'s that applies) holds a value that is not an
object; a schema that nests combinators and references more than 8 levels deep or
holds more than 2,000 schemas; and functions whose schemas together take more than
100,000 steps to read (a step is a schema read, a reference followed, a property
or a listed name met, an enum entry compared): the work of naming the arguments is
bounded by the request, before a slot is held.

**Logs and errors.** One line per chat completion or image request names the
request id, key
id, profile, model, status, duration, how many knowledge excerpts were
added (chat only) and, for `auto`, who chose the model, plus, for a failure, the
operator-only detail (a Go error or a
runtime's error code; for an image request, whether it made images or not, how
many of the files it looked at the collection left out and why, never a file
name). A failure to list models, a failed knowledge search and
a failure to verify a key are logged too; a successful listing is not. None
of the gateway's lines holds a prompt, an answer or a key. monomind's own
diagnostics (its stderr) reach the server log as they do for any use, and
nothing in this repo shows that they never echo a prompt. A failed
authentication is logged with the address of the connection, never the key it
sent (behind a reverse proxy that is the proxy's address: `X-Forwarded-For` is
not read); a request that sent no credential at all is not logged. Error
messages sent to callers are generic for internal failures and for a runtime
error other than a setup hint, a rate limit, quota or a timeout; of those,
only the part monomind classified reaches the caller, on one line and at most
300 characters. The one other place a runtime's words reach the caller is the
502 of an image request whose turn made no image: what the runtime replied, on
one line and at most 300 characters, the answer to the caller's own prompt.
Every response of the four routes carries an `X-Request-Id`
to quote to the operator. A path or method the API does not have gets Go's
plain-text 404 or 405, without one. `GET /health` on the dedicated listener is
unauthenticated and returns the server version.

**What this does not cover.**

- Revocation applies to the next request: a turn already running finishes,
  within the turn timeout. Stopping the daemon or `httpapi` ends the turns in
  flight (their clients get a 503 they can retry) and waits for their
  processes to be killed, up to a few seconds longer than monomind's kill grace
  when a second Ctrl+C forces the exit, so that no agent CLI outlives the
  command. `httpapi` and a dedicated listener first give running requests 10
  seconds to finish; the daemon's main listener does not.
- The agent CLIs keep session transcripts of their turns in their own stores
  (claude's `~/.claude/projects/<folder>`), prompts and answers included, as
  they do for any use, and the arguments and results of the tool calls of a
  conversation with tools. The fixed slot folders keep the number of those
  folders bounded (one per profile and slot), not their content, and keep
  one profile's apart from another's.
- Some runtimes (antigravity) pass the prompt on their command line, which
  other local users can read with `ps` while the turn runs. So does a context
  key's knowledge search, for every runtime, claude included: it runs
  `monomind mcp exec -t knowledge_search` with the first 500 characters of the
  last user message in its arguments, for up to 30 seconds, in two parallel
  processes (documents and captures). On a shared host, run the server on a
  machine or an OS user of its own.
- A turn's prompt and system prompt (which carry a context key's excerpts) are
  written to files in a private folder (mode 0700) under
  `~/.monoagent/workspaces/api/.tmp`, outside every turn's writable area, and
  removed when the turn ends (a crash leaves them until the next start, which
  clears the folder). The mode keeps other OS users out, not other turns: a
  runtime that can read the disk can read them while the turn runs, another
  profile's included, and an unconfined runtime can write there.
- monomind keeps copies of its own: its hermes, cline and kimicode runners write
  the prompt, or an agent file, under the temp directory and hand the CLI its
  path. A sandboxed turn can write the system temp directory and `/tmp`
  (checked with codex's `workspace-write` on macOS), so it could rewrite such a
  copy before it is read. Each API turn therefore gets a temp directory of its
  own (`TMPDIR`, `TMP` and `TEMP`), a folder inside its own folder that no other
  turn's sandbox reaches, emptied with the rest of the turn's files. That
  protects the turns of the API, and only those. A turn that does not come
  through it (a workflow, a chat) keeps monomind's copies in the system temp
  directory, and a sandboxed API turn can still write `/tmp`: where that is the
  temp directory (Linux), it could rewrite such a turn's prompt file. On a host
  that also runs hermes, cline or kimicode turns as the same OS user, run the
  API server as a user of its own, or with `--confinement chat-only`. The files
  `monomind.Exec` itself makes for every other caller go to `~/.monoagent/tmp`
  (mode 0700, files older than a day swept), or to the system temp directory
  when that folder cannot be made.
- A runtime can leave a process behind it (a command started with `nohup`, say)
  that keeps its write access to the turn's folder. Emptying the folder does
  not stop it. For an image turn a process that keeps writing where it wrote
  before cannot put a file into a later request's response, which is read from a
  folder with a name it did not know in advance; one that looks (lists the slot
  folder, reads the name) can still read and write whatever that folder holds
  while the turn runs, as a process of the same OS user (see Image generation). A sandbox that only confines writes below the turn's folder
  (macOS's, checked) still lets the turn remove that folder and put a link in
  its place. The gateway looks at the folder from its parent, which no turn can
  change, and acts on it through open handles: a link is set aside, not
  followed, so neither such a process nor such a turn can send the emptying
  outside the folder. A folder it makes impossible to empty (a tree deeper
  than 100 levels), or too slow to (more than 30 seconds: it keeps changing the
  tree, or swaps a directory for a FIFO that the walk then waits on) is moved to
  `~/.monoagent/workspaces/api/.quarantine` and replaced. A walk that was given
  up on is not waited for; one stuck on a FIFO holds a thread and a few file
  descriptors until the server restarts. Nothing ends the process. Run the server as a dedicated OS user and
  look at what it leaves.
- Where a turn's sandbox is rooted is a path: the turn's folder. A process that
  outlives its turn can replace that folder with a link in the moment between
  the gateway's last look at it and the start of the next turn of the slot, and
  that turn's sandbox is then rooted where the link points, an OS user's home
  included. The gateway looks right before it starts the process, which narrows
  the window to milliseconds and cannot close it: only a runtime that roots its
  sandbox on an open directory could. Read `sandboxed` as confining a prompt,
  not a determined key holder, and keep the OS user's own files out of reach (a
  dedicated user, as above).

**Not part of this surface.** Running a caller's tool: the gateway returns the
call and the caller runs it. The runtime's own native tools are a separate matter,
covered by the classes above.
