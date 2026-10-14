package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

func refOrgCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "org",
		Short: "Orgs, automations, grants, automation roles, autonomy, and holding orgs",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║        monoagentcli — Orgs and automations (org …)           ║
╚══════════════════════════════════════════════════════════════╝

An org is a team of agent roles run by monomind. Its config is
<profile folder>/.monomind/orgs/<name>.json; org commands use the active
profile's folder unless --project is given. Every org command prints JSON.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
PROCESSES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  monoagentcli org serve            start monomind's org daemon for the folder
  monoagentcli org serve --stop     stop the folder's running orgs and daemon
  monoagentcli daemon               engine + HTTP API + automation-role endpoint
                                    + autonomy decisions (writes a heartbeat)
  monoagentcli --json status        both daemons' state

  Without the daemon: granted tools return daemon_required, automation
  roles cannot run, and every org behaves as autonomy level manual.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
SIGNED DEFINITIONS (monomind 2.21+)
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org sign <org> --status           signed | unsigned | changed | ...
  org sign <org>                    monomind's review; signs nothing
                                    (asks first on a terminal)
  org sign <org> --yes [--expect-hash <hash>]    sign (only that definition)

  monomind runs or reloads an org only when the operator signed its
  definition (everything but goal, status and role titles,
  responsibilities and layout). monoagentcli re-signs its own edits of
  a signed org; an org edited any other way, imported, written whole
  with create-json, or changed from inside a coding agent is left
  unsigned with a warning until you review and sign it (the review's
  "hash" is what --expect-hash signs). org run refuses such an org with
  code org_not_signed, and
  org status shows each org's "signature".

  Orgs under a symlinked .monomind (or with a symlink on the way to an
  instructions file), on a network or FUSE/FAT filesystem, or on Windows
  are never re-signed automatically, and their review hands over no hash
  unless monomind 2.22 reviews and signs them itself: sign those with
  "monomind org sign <org>" in a terminal.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
AUTOMATIONS AND GRANTS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org automation list|unassigned
  org automation add <org> --workflow <id> --alias <alias> [--owned]
  org automation remove <org> --alias <alias>

  org grant list <org> [--role <id>]
  org grant add <org> --role <id> --automation <alias>
      [--mode run|trigger|status] [--wait=true|false] [--timeout 600]
      [--approval none|required] [--max-calls-per-run 20]
      [--max-calls-per-day 200] [--max-output-bytes 16384]
  org grant remove <org> --role <id> --automation <alias>
  org effective-tools <org> --role <id>

  A grant gives the role the tool monoagent__automation_<alias>. Aliases:
  a lowercase letter, then letters, digits, or underscores; not human,
  workflow, status, or output. Workflows with outbound nodes default to
  --approval required (each call is a decision). The first grant of a
  role pre-fills denyTools Bash.

  The tool's arguments reach the workflow as input in its trigger data,
  and each field is also copied to the top level ($json.<field>), never
  over org, input, trace or the other keys the run sets. monomind passes
  only the arguments a tool's schema lists, so the tool lists the fields
  the workflow's templates read (input.<field>, and $json.<field> in the
  nodes the trigger feeds); a workflow that reads its input some other
  way (a code node) needs an input_schema on the role's automations entry
  in the org file.

  A granted run gets the calling role's workdir (its run_config.workspace
  directory) as org.workdir in its trigger data; so does an automation
  role's run started by a message from a role. File nodes (spreadsheet,
  write file, image, attachments, uploads, FTP local paths, browser
  uploads) then refuse any path that resolves outside it, after following
  .. and symlinks; relative paths are taken from the workdir. Shell
  commands cannot be confined. automation list, grant add/list and
  effective-tools report file_input_nodes: nodes whose paths come from
  the run's input.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
AUTOMATION ROLES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org automation-role add <org> --alias <alias> --reports-to <role>
      [--title T] [--reply last_node|node:<name>]
  org automation-role remove <org> --role <id>
  org automation-role rotate <org> --role <id>

  Messages to the role start the workflow (trigger data: org_message,
  text, trace); when the run ends its output is sent back to the sender
  as "re: <subject>".

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
WORKFLOW NODES
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org.run      start an org run and wait (wait:false returns at once;
               exclusive:true refuses to join a run it did not start)
  org.send     message a role; output says delivery live or queued
  org.ask      ask a role and pause until it replies (the workflow must
               be an automation role of that org)
  trigger.org  start on org events (event_types, role, subject_match,
               question_kind ask_human|approval|any) or on messages to
               this workflow's automation role (mode endpoint)

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
MESSAGES AND LIFECYCLE
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org send <org> --to <role> [--from org:role] --subject S --body B
  org chat send <org> -- <text>     message the boss as human:operator
  org chat history <org> [--run R] [--limit N]
                                    the boss thread: your messages, the boss's
                                    replies, questions, approvals, gates
                                    (pending or how they ended), team rows
  org chat answer <org> <questionId> -- <answer>
  org chat approve|deny <org> <gate-id|request-id|role:action> [-- note]
                                    idempotent ("already": true when resolved
                                    before); refused (exit 3) while the org is
                                    not running, and nothing is sent
  org queued <org>                  messages waiting for the org's next start
                                    (read-only view of monomind's inbox.jsonl)
  org stop|pause|resume <org>
  org rename <org> <new>            org names are unique on this machine
  org delete <org> [--force]
  org legacy list | legacy move <org>
  org reconcile                     rewrite every org file's generated blocks
                                    from the grant rows (after a folder move)
  org teardown-profile [--dry-run]  the org half of deleting a profile: revoke
                                    its grants, endpoints, autonomy and API
                                    keys; stop its orgs and org daemon

  Crossings carry "[trace chn_… hop=N]" and stop at run_config.max_hops
  (default 8) or max_repeats calls to one target per minute (default 20).
  Granted calls on one chain add one hop per 8, whichever roles make them.
  Org-started runs continue a chain; a webhook run continues one only from
  a signed X-Monoagent-Trace header (sent by http.request), else it starts
  fresh. A loop through a webhook is refused (HTTP 429) at the hop limit
  of the org the chain started in; the token expires after an hour.
  Webhook and trigger.org runs are limited to 200 per workflow per minute
  (HTTP 429 for a webhook).

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
AUTONOMY
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  org autonomy show <org>
  org autonomy set <org> [--level manual|mid|full]
      [--decider model|boss|parent|jev] [--decider-model M] [--decider-runtime R]
      [--decider-timeout 120] [--decider-threshold 0.8]
      [--policy TEXT | --policy-file F]
      [--tier <class>=routine|consequential|irreversible]... [--clear-tier C]...
      [--on-decider-failure deny|human] [--max-decisions N] [--max-decider-usd X]
  org autonomy pause <org>|--all [--for 30m]     resume <org>|--all
  org autonomy decisions <org> [--run R] [--verdict V] [--limit N]
  org autonomy needs-you <org>

                  routine        consequential   irreversible
    manual        person         person          person
    mid           rule approves  decider         person
    full          rule approves  decider         decider

  Classes and default tiers: tool:<Name> routine (tool:Bash consequential
  for a role with grants and Bash allowed), org_complete, question, and
  org_start consequential, gate irreversible, grant:<alias> and
  hil:<alias> by the workflow (irreversible with outbound nodes).
  Wildcards tool:* and grant:* are accepted.

  Deciders: model (a one-shot agent turn), boss (the org's root role),
  parent (a holding org's Initiator), jev (TypeSafe Jev picks approve /
  deny / escalate in one ~0.3 s call; questions, answers whose top
  probability is below --decider-threshold (default 0.8), and Jev errors
  go to the model decider, so --decider-model/--decider-runtime still
  apply). jev needs a TypeSafe key for the profile (vault secret
  "typesafe" or TYPESAFE_API_KEY); choosing it is the opt-in — no
  "jev enable" needed. It sends the org name and goal, requester title,
  item facts, and the agent-written text (marked untrusted) to TypeSafe. Tiers and
  routes above never change. decisions shows its confidence and
  probabilities.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
HOLDING ORGS
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  "kind": "holding", "children": [{"org":"sales","start":"on_demand",
  "budget_share":0.5}], run_config.group_budget_usd

  org group init <holding>          Initiator tools + report-up lines
  org group start|stop|status <holding>

  A child belongs to at most one holding org, in the same profile.

  A message to a stopped child starts a full run of the child's own goal,
  so phrase child goals for messages: "Handle requests from hq; when idle,
  complete." and use org_start(org, task) for task-specific runs.
  validate, create-json, and group init add a non-fatal entry to
  "warnings" for a child goal that lacks a request word (request, message,
  ask, inquiry, respond, reply) or lacks the parent's name, idle, or
  complete. run_config.idle_minutes (default 10) keeps a woken child short.

`)
		},
	}
}
