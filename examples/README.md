# Example Workflows

Eleven ready-to-import, structurally valid workflow JSONs. None of them use
social-platform nodes — they work with the default build
(`go build ./cmd/monoagentcli`; none of them need the social platform nodes).

Every node `type` matches a schema in `internal/workflow/schemas/` and every
config key matches that schema's fields. Values marked
`REPLACE_WITH_*` (and empty `credential_id`s) must be filled in before a run
will succeed — `workflow import` and `--dry-run` work without them.

## Catalog

| File | What it does | Trigger | Nodes |
|---|---|---|---|
| `morning-briefing.json` | Weekday 7am: RSS → filter → one article list → AI briefing → human review → email | `trigger.schedule` | `system.rss_read` → `core.filter` → `core.code` → `agent.ask` → `core.human_in_loop` → `comm.email_send` |
| `agent-builder-migration.json` | Starter for leaving OpenAI Agent Builder: message in → AI triage → AI draft reply → human approval → email | `trigger.webhook` | `agent.ask` → `agent.ask` → `core.human_in_loop` → `comm.email_send` |
| `sheets-to-gmail.json` | Read pending rows from a Sheet and email a follow-up to the first one (one per run) | `trigger.manual` | `service.google_sheets` → `core.filter` → `core.limit` → `comm.email_send` |
| `http-to-slack.json` | JSON POST in → formatted message → Slack channel | `trigger.webhook` | `core.set` → `comm.slack` |
| `github-issues-to-linear.json` | GitHub "issue opened" webhook → matching Linear issue | `trigger.webhook` | `core.filter` → `service.linear` |
| `webhook-to-postgres.json` | JSON event in → normalize → insert row into Postgres | `trigger.webhook` | `core.set` → `db.postgres` |
| `stripe-events-to-sheets.json` | Stripe-style event in → append row to a Sheets ledger | `trigger.webhook` | `service.google_sheets` |
| `image-gen-vault.json` | Generate an image with Gemini (your browser session), lands in the local image vault | `trigger.manual` | `gemini.generate_image` |
| `outlook-inbox-sync.json` | Hourly: sync Outlook inbox + sent mail into People (adapted from the bundled template) | `trigger.schedule` | `service.outlook_mail` → `people.sync_outlook_message` (×2 branches) |
| `telegram-notify.json` | Every morning: RSS items pushed as one Telegram bot message | `trigger.schedule` | `system.rss_read` → `core.code` → `comm.telegram` |
| `data-pipeline.json` | Fetch API JSON → reshape in code → write CSV → sum totals | `trigger.manual` | `http.request` → `core.code` → `data.spreadsheet` → `core.aggregate` |

Every example except `image-gen-vault.json` and `outlook-inbox-sync.json`
(which adapt the bundled `gemimg` / `outlook_email_sync` templates) and
`agent-builder-migration.json` (a starter to adapt, not run as-is) is also
bundled as a template — `monoagentcli workflow templates list` — under the
file name with underscores (e.g. `morning_briefing`). A test keeps the two
copies identical.

## Quickstart

```bash
# 1. Import any example (prints the new workflow id):
monoagentcli workflow import --file examples/http-to-slack.json

# 2. Check it without running anything:
monoagentcli workflow validate --file examples/http-to-slack.json
monoagentcli workflow run <id> --dry-run

# 3. Activate the workflow so its triggers are served:
monoagentcli workflow activate <id>

# 4. Start the daemon AFTER activation (or restart it) and keep it running:
monoagentcli daemon
```

Note: an activated workflow's triggers are only served by a daemon started
(or restarted) **after** the `workflow activate` step — if the daemon was
already running, restart it.

## Firing the webhook examples

The daemon serves webhook triggers at `http://127.0.0.1:9321/webhook/<path>`
(default bind is loopback-only — see `internal/workflow/engine.go`). The
`<path>` segment is the trigger node's `path` config value, e.g. `"alerts"`
below.

First activate the workflow (`monoagentcli workflow activate <id>`), then
run the daemon — triggers activated while a daemon is already running need
a daemon restart before they fire.

With `monoagentcli daemon` running in another terminal:

```bash
# http-to-slack.json — path "alerts", requires the X-Webhook-Secret header:
curl -s http://127.0.0.1:9321/webhook/alerts \
  -H 'Content-Type: application/json' \
  -H 'X-Webhook-Secret: REPLACE_WITH_SECRET' \
  -d '{"event":"deploy_failed","severity":"high","source":"ci"}'

# webhook-to-postgres.json — path "events", no auth configured:
curl -s http://127.0.0.1:9321/webhook/events \
  -H 'Content-Type: application/json' \
  -d '{"event":"signup","userId":"u_123","data":{"plan":"pro"}}'

# stripe-events-to-sheets.json — path "stripe":
curl -s http://127.0.0.1:9321/webhook/stripe \
  -H 'Content-Type: application/json' \
  -H 'X-Webhook-Secret: REPLACE_WITH_SECRET' \
  -d '{"id":"evt_1","type":"payment_intent.succeeded","amount":4200,"currency":"usd"}'
```

A `{"success":true}` response means the workflow was triggered; check the
execution with `monoagentcli workflow executions <id>`.

### Binding the webhook server remotely

The default bind (`127.0.0.1:9321`, loopback-only) is same-user trust: no
TLS, no extra ceremony, and it can't be reached from another machine.
Override it with `MONOAGENT_WEBHOOK_ADDR` (e.g. `0.0.0.0:9321`) when the
daemon runs somewhere callers need to reach over the network — a Docker
container, a VM, a LAN box.

**Any non-loopback bind is always served over TLS.** If you don't configure
a certificate, the server auto-generates and caches a self-signed one under
`~/.monoagent/webhook-tls/` (covers `localhost`/`127.0.0.1`/`::1` — a
caller connecting by hostname or LAN IP must skip certificate verification,
e.g. `curl -k`). There is no way to make a non-loopback bind serve plain
HTTP — the server refuses rather than exposing webhook payloads (which can
carry caller-attached auth headers/tokens) unencrypted on the network.

For a real certificate instead of the self-signed one, set:

```bash
export MONOAGENT_WEBHOOK_TLS_CERT=/path/to/cert.pem
export MONOAGENT_WEBHOOK_TLS_KEY=/path/to/key.pem
```

(Both or neither — setting only one is a startup error.)

Either way, once bound remotely, requests need `https://` and, for the
self-signed cert, `-k`/`--insecure` (or trust the generated cert
explicitly) — otherwise the same request shapes as the "Firing the webhook
examples" section above, just against `https://<host>:9321/...` instead of
loopback.

**Authentication is still the workflow's job, not the server's.** TLS
protects the payload in transit; it does not authenticate the caller. Give
every remotely-reachable webhook trigger node an auth header/token pair or
an HMAC secret (see the `http-to-slack.json` / `stripe-events-to-sheets.json`
examples above) — anyone who can reach the port and guesses the `<path>`
segment can otherwise fire it. Pair this with
`MONOAGENT_WEBHOOK_ALLOWED_ORIGINS` (a comma-separated origin allowlist) if
browser-based callers need to reach it; without it the server emits no CORS
headers at all, so cross-origin browser requests are blocked outright.

## Notes

- **Credentials**: nodes with a `credential_id` field need a connection first —
  see `monoagentcli ref connections`. Secrets can be piped in via stdin
  (`monoagentcli secret add --kind secret --name ...`), never argv.
- **AI steps**: `agent.ask` runs on a local agent CLI through monomind — no API
  key. Check what you have with `monoagentcli agent scan --installed` and set
  the node's `runtime` to one of them (see "How AI works in Mono Agent" in the
  root README). `gemini.generate_image` needs `monoagentcli login gemini` once.
- **Cron format** is 6-field (seconds first): `0 0 7 * * 1-5` = weekdays 07:00.
- **Docker**: see `docker-compose.yml` in the repo root for running the daemon
  as a container with persistent state.
