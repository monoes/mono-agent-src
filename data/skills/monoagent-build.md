---
name: monoagent-build
description: Build, test and fix a monoagent workflow end to end — find an existing template or workflow first, look up node types and their config schemas, write the workflow JSON, validate it, test single nodes, dry-run, run it and read every node's output, then fix what failed and repeat. Invoke when the user asks to create, build, automate, wire up, debug or repair a monoagent workflow or automation (not just run an existing one — that is monoagent-workflows).
---

# Building monoagent workflows

`monoagentcli` is a global CLI; all state lives in `~/.monoagent/`, so every
command works from any directory. Put `--json` **before** the subcommand
(`monoagentcli --profile <profile> --json workflow validate ...`) whenever you will parse the
output. `monoagentcli ref workflow`, `ref nodes`, `ref node <type>`,
`ref expressions` and `ref examples` are offline and always match the
installed version: read them instead of guessing.

Commands that act on a profile take an explicit `--profile <profile>` (an id or
name; `monoagentcli profile list` shows them, and ask the user which one if
more than one fits). The examples write it as `--profile <profile>`. There is no
active profile, and a command without it exits 3 and lists the profiles.

The loop, in order:

1. **Find** something that already does it.
2. **Look up** the node types and their fields.
3. **Write** the workflow JSON.
4. **Validate** it and fix every issue.
5. **Test single nodes** you are unsure of.
6. **Dry-run**, then **run** with `--json`.
7. **Read** each node's status and output; fix by the error; go back to 4.

## 1. Find before you build

```bash
monoagentcli --profile <profile> workflow search [query]          # templates + saved workflows, each with its run command
monoagentcli workflow templates list
monoagentcli workflow templates show <id>     # inputs, nodes, exact run command
monoagentcli --profile <profile> workflow templates use <id>      # save an editable copy (starts inactive)
monoagentcli --profile <profile> workflow list
monoagentcli --profile <profile> workflow get <id>                # a saved workflow as JSON (the shape `save` reads)
```

A template copy or an existing workflow you edit is almost always faster and
better wired than one built from scratch. Only hand-build what nothing covers.

## 2. Node types and their fields

```bash
monoagentcli node list [--filter http]        # every node type
monoagentcli node schema <type>               # config fields: key, type, required, default, options, help
monoagentcli ref node <type>                  # longer docs for one node type
monoagentcli node palette                     # all types by category, with schemas (JSON)
```

Use exactly the `key` names from `node schema`; a field marked `required`
must be set. Branching nodes route by output handle: `core.if` emits on
`true` / `false`, `core.switch` on its case handles; everything else on
`main`. Triggers start a workflow: `trigger.manual` (run by hand or
`--input`), `trigger.schedule` (6-field cron, needs `monoagentcli daemon`),
`trigger.webhook`.

## 3. Write the workflow JSON

```json
{
  "name": "Fetch and tag",
  "description": "GET a URL and add a field",
  "nodes": [
    {"id": "t1", "type": "trigger.manual", "name": "Start", "config": {}},
    {"id": "n1", "type": "http.request", "name": "Fetch",
     "config": {"method": "GET", "url": "{{ $json.url }}"}},
    {"id": "n2", "type": "core.set", "name": "Tag",
     "config": {"assignments": [{"field": "source", "value": "{{ $node[\"Fetch\"].json.url }}"}]}}
  ],
  "connections": [
    {"source": "t1", "source_handle": "main", "target": "n1", "target_handle": "main"},
    {"source": "n1", "source_handle": "main", "target": "n2", "target_handle": "main"}
  ]
}
```

- Node `id`s are unique within the workflow; connections reference them.
- An unset handle means `main`.
- Expressions: `{{ $json.field }}` (current item), `{{ $node["Name"].json.f }}`
  (an earlier node's first output item, by its **name**), helpers such as
  `upper`, `default`, `json`, `jsonParse` (see `ref expressions`).
- Trigger data from `workflow run --input '{...}'` arrives as `$json`;
  `monoagentcli --profile <profile> workflow inputs <id>` lists the fields a workflow reads.

Save it:

```bash
monoagentcli --profile <profile> workflow save --file wf.json     # no "id": creates; with "id": replaces that workflow
monoagentcli --profile <profile> workflow import --file wf.json   # import a file (bundled automations: --yes)
```

Or build it step by step:

```bash
monoagentcli --profile <profile> workflow create "Fetch and tag" --description "..."
monoagentcli --profile <profile> workflow node add <wf-id> --type http.request --name Fetch --config '{"method":"GET","url":"..."}'
monoagentcli --profile <profile> workflow node list <wf-id>
monoagentcli --profile <profile> workflow node set <wf-id> <node-id> --config '{...}'   # or --name, --x/--y
monoagentcli --profile <profile> workflow connect <wf-id> --from <nodeID>:main --to <nodeID>:main
monoagentcli --profile <profile> workflow disconnect <wf-id> <connection-id>
```

## 4. Validate

```bash
monoagentcli --profile <profile> --json workflow validate --file wf.json   # before saving
monoagentcli --profile <profile> --json workflow validate <wf-id>          # a saved workflow
```

Exit 0 valid, 2 not found, 3 invalid. The JSON has `valid`, `errors[]`
(messages) and `warnings[]` (e.g. an unknown node type: a typo, or an
automation that is not installed). Newer builds also return `issues[]`, one
per problem, with `node_id`, `field`, `code` and `hint`: go to that node, fix
that field, follow the hint. Fix **all** errors, then validate again; never
run a workflow that does not validate.

## 5. Test single nodes

Run one node type on its own with the config you intend to use and a sample
input item, before wiring it in:

```bash
monoagentcli --profile <profile> node run http.request --config '{"method":"GET","url":"https://httpbin.org/get"}' --output json
monoagentcli --profile <profile> node run core.set --config '{"assignments":[{"field":"x","value":"{{ $json.a }}"}]}' \
  --input '[{"json":{"a":1}}]' --output json
echo '{"config":{...},"input":[...]}' | monoagentcli --profile <profile> node run <type> --stdin   # keeps secrets out of argv
```

Credentials resolve from saved connections; `--credential <id|platform>`
overrides. Browser/social nodes need `monoagentcli --profile <profile> login <platform>` first.
The output items you see here are what the next node gets as `$json`.

## 6. Dry-run, then run

```bash
monoagentcli --profile <profile> workflow run <wf-id> --dry-run                    # validate + execution order, nothing runs
monoagentcli --profile <profile> --json workflow run <wf-id> --input '{"url":"https://example.com"}'
monoagentcli --profile <profile> workflow run <wf-id> --no-wait                    # long runs: prints the execution id
monoagentcli --profile <profile> workflow run <wf-id> --timeout 30m
```

`--json` prints the execution record with each node's status and output
items once the run ends (credential-like values masked; `--full-outputs`,
which unmasks them, is the user's own flag: an agent's run is refused). Status `waiting` means a human-in-the-loop node paused it:
`monoagentcli --profile <profile> hil list`.

## 7. Read the result and fix

```bash
monoagentcli --profile <profile> --json workflow executions <wf-id> --limit 5
monoagentcli --profile <profile> --json workflow execution <execution-id>
```

`workflow execution` gives `status`, `error_message` and `nodes[]`, each with
`node_id`, `node_name`, `status`, `error_message`, `input_items`,
`output_items` and `retry_count`. Find the first node that failed (or the
first whose `output_items` are empty or wrong) and fix by its error:

| Error looks like | Fix |
|------------------|-----|
| missing/required field, invalid config | `node schema <type>`; set the field with `workflow node set <wf-id> <node-id> --config` |
| unknown node type | `node list` for the exact type; install the automation it needs |
| template/expression error, `<no value>` | the field is not on `$json`: check the upstream node's `output_items`, use `$node["Name"]` |
| credential / 401 / 403 / no session | `monoagentcli --profile <profile> connect list`, `monoagentcli --profile <profile> login <platform>`; pass `--credential` to `node run` to test |
| timeout | raise the node's timeout field or `--timeout`; test the node alone |
| rate limit / 429 | slow down (fewer items, a delay node), run again later |
| empty output, no error | the upstream node returned nothing, or an `if` routed to the other handle |

Then validate again (step 4) and re-run. Change one thing at a time and
re-test that node with `node run` before running the whole workflow.

When it works: `monoagentcli --profile <profile> workflow activate <wf-id>` for schedule/webhook
triggers (and keep `monoagentcli daemon` running).

## MCP tool equivalents

When the `monoagent` MCP server is connected (registered by
`monoagentcli --profile <profile> doctor` / `setup --mcp` with `--allow-mutations`), prefer its
tools. Read-only: `docs` (the `ref` topics), `workflow_list`, `workflow_get`,
`template_list`, `node_list`, `node_schema`, `workflow_validate` (`id`,
`file` or an inline `workflow`), `workflow_status` (an execution by
`execution_id`, with per-node output items), `hil_list`. Mutating:
`workflow_create`, `workflow_node_add`, `workflow_run` (returns
`execution_id`, `status`, per-node `output_items`), `workflow_set_active`,
`workflow_delete`, `hil_approve`, `hil_reject`. Newer builds add
`workflow_connect`, `workflow_save` and `node_run`; if `tools/list` does not
show one, use the CLI command above instead.

The same loop over MCP: `template_list` / `workflow_list` → `node_list` →
`node_schema` → `workflow_create` + `workflow_node_add` + `workflow_connect`
(or `workflow_save` with the whole document) → `workflow_validate` →
`node_run` → `workflow_run` → `workflow_status` → fix → repeat.

## Rules

- Never print, log or paste secret values; use saved connections and the vault.
- Live social/messaging nodes act for real: test with explicit small limits.
- Ask before activating a workflow or running one that sends, posts or pays.
