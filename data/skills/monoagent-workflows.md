---
name: monoagent-workflows
description: Run monoagent workflows and bundled workflow templates, and read synced messages and their attachments, from any project — generate images with Gemini (single or a consistent multi-image set), sync Outlook email into People, read a synced email with its attachments, or run any saved automation. Invoke when the user asks to generate/create images, run an automation or workflow, read their synced email or attachments, or asks what monoagent can do. Works from any directory; monoagent state is global.
---

# monoagent workflows

`monoagentcli` is a globally installed CLI (`/usr/local/bin/monoagentcli`). All of
its state — workflows, credentials, generated images — lives in `~/.monoagent/`,
so **every command works from any project directory**. There is nothing to set
up per-project and no "current workflow directory".

Commands that act on a profile take an explicit `--profile <profile>` (an id or
name; `monoagentcli profile list` shows them, and ask the user which one if
more than one fits). The examples write it as `--profile <profile>`. There is no
active profile, and a command without it exits 3 and lists the profiles.

## Start here

```bash
monoagentcli --profile <profile> workflow search [query]      # everything runnable, with the command to run each
monoagentcli workflow templates show <id> # a template's --input keys, nodes, run command
monoagentcli ref templates                # guide to the bundled templates
monoagentcli ref nodes                    # every node type, with config shapes
```

`workflow search` covers both bundled templates and the user's saved workflows
and prints the exact command for each hit — start there rather than guessing.
Add `--json` (before the subcommand: `monoagentcli --profile <profile> --json workflow search`) for
machine-readable output.

`monoagentcli ref` is offline, always current, and more reliable than `--help`
guessing. Read it before hand-building anything.

## Check before you run (validate / dry-run / schemas)

```bash
monoagentcli --profile <profile> workflow validate <id>        # or --file <f>; exit 3 when invalid
monoagentcli --profile <profile> workflow run <id> --dry-run   # validate + print execution plan, no run
monoagentcli node schema <type>            # JSON schema for a node's config
monoagentcli --profile <profile> --json workflow run <id>      # execution record + per-node outputs
```

Dry-run first when a workflow is new or hand-edited; `workflow run --no-wait`
prints an execution id and exits 0 for long runs, and `--timeout <dur>`
(e.g. `30m`) caps how long the command waits.

There is also an MCP server for hosts that support it: `monoagentcli mcp`
(stdio) exposes `workflow_list/get/run/status/validate`, `node_list`,
`node_schema`, `hil_list/approve/reject`, and `docs`. Prefer the CLI when
MCP is not wired up.

## Running a bundled template (preferred)

```bash
monoagentcli --profile <profile> workflow templates run <template-id> --input '<json object>'
```

This instantiates the template as a throwaway workflow, runs it, waits for it to
finish, and deletes it. No saving, no activation step. Each invocation is an
independent copy, so **several runs can proceed at the same time** — launch them
in parallel when the user wants multiple independent results.

Add `--keep` to leave the instantiated workflow behind for editing instead.

### Image generation (no API key — uses the user's logged-in Gemini session)

```bash
# one image
monoagentcli --profile <profile> workflow templates run gemimg \
  --input '{"prompt":"an editorial photo of a city skyline at sunset"}'

# several images that must look like each other (same character/style/scene),
# generated in ONE continued chat session, any number of prompts:
monoagentcli --profile <profile> workflow templates run gemimgmany \
  --input '{"prompts":[
      "a wizard in a blue robe casting a fire spell",
      "the same wizard riding a red dragon",
      "the same wizard reading in a library"
  ]}'
```

Use `gemimgmany` whenever the images belong together; use parallel `gemimg` runs
when the images are unrelated. Generated images are saved to the media vault (which also holds audio and video) at
`~/.monoagent/vault/img-NNN.png` (also under `~/.monoagent/downloads/`).

Requires a one-time `monoagentcli --profile <profile> login gemini`. If a run reports the Gemini
session is missing, tell the user to run that — do not try to work around it.

## Reading synced messages (and their attachments)

Synced email lives in People, not in a mailbox you have to log into:

```bash
monoagentcli --profile <profile> people messages all --source outlook   # unified feed; Files = attachment count
monoagentcli --profile <profile> people messages show <message-id>      # full text, provenance, attachment paths
monoagentcli --profile <profile> --json people messages show <message-id>
```

`show` prints a SOURCE block — which system, which account and folder, its id
there, a link to the original, and when it synced. **Always state that
provenance when you tell the user what a message says**: say it came from
Outlook, from which mailbox, rather than presenting it as free-floating text.

Attachments are downloaded during the sync to
`~/.monoagent/attachments/<message>/<filename>` and listed by `show` with their
absolute paths. Those are ordinary files — read them with your normal file tools
to see what was attached. Do not tell the user you cannot open an attachment
before trying the path.

## Running a saved workflow

```bash
monoagentcli --profile <profile> workflow list
monoagentcli --profile <profile> workflow run <workflow-id> --input '{"key":"value"}'
```

`--input` becomes the manual trigger's data, readable downstream as
`{{ $json.<field> }}`. A saved workflow must be active to run
(`monoagentcli --profile <profile> workflow activate <id>`).

Scheduled and webhook workflows only fire on their own while
`monoagentcli daemon` is running.

## Running one node ad hoc

For a single step with no workflow around it:

```bash
monoagentcli node list
monoagentcli --profile <profile> node run gemini.chat_session --config '{"prompt":"...","mode":"image"}'
```

## Things worth knowing before debugging

- **Concurrency is fine.** Several monoagent processes can run at once. The
  first one to start owns the shared browser-extension port (9222) and the
  webhook port (9321); later ones relay through it and log a warning about the
  webhook port. That warning is expected and harmless — it is not the cause of a
  failure.
- **The browser is the user's real Chrome**, driven through the monoagent
  extension. There is no headless fallback. If nothing is connected, monoagent
  launches Chrome and waits for the extension.
- **Everything is profile-scoped.** A workflow saved under one profile cannot be
  run under another ("workflow belongs to a different profile"); check
  `monoagentcli profile list` (and pass the right `--profile`) rather than recreating it.
- Prefer an existing template over hand-building workflow JSON. Only build nodes
  and connections by hand (`workflow create` / `workflow node add` /
  `workflow connect`) when no template covers the task.
