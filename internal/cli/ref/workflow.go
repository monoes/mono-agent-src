package ref

import (
	"fmt"

	"github.com/spf13/cobra"
)

// ── ref workflow ──────────────────────────────────────────────────────────────

func refWorkflowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "workflow",
		Short: "Workflow JSON structure and connection format",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Print(`
╔══════════════════════════════════════════════════════════════╗
║                monoagentcli — workflow format reference            ║
╚══════════════════════════════════════════════════════════════╝

BEFORE HAND-BUILDING ONE — CHECK FOR A BUNDLED TEMPLATE FIRST
  monoagentcli workflow templates list
  monoagentcli --profile <profile> workflow templates run <id> --input '{...}'   # one-off, nothing saved
  monoagentcli --profile <profile> workflow templates use <id>                   # save an editable copy

Instantiates a ready-made, pre-wired workflow (fresh node/connection IDs,
correct config shape) for the --profile profile in one command — starts
inactive so you can fill in credentials before 'workflow activate'. Only
fall back to building the JSON below by hand for something not covered by
an existing template.

FILE LOCATION
  ~/.monoagent/workflows/<workflow-id>.json
  The filename MUST match the workflow's "id" field exactly.
  Editing the file of a workflow that already exists is supported. The next command that opens
  the workflows reads it, and when the definition differs from what is stored it records the new
  content as a version noted "edited outside the app". The file itself is never rewritten by that
  step, and a file that does not parse is left alone ('monoagentcli doctor' lists it). An imported
  edit removes the workflow's earlier approvals, so the edited workflow runs as unapproved until
  you approve it. A file is not read while a save of the same workflow is in progress.

  A NEW file written into the folder is not picked up by itself (it could name any profile).
  Create workflows with 'workflow import --file <json>', 'workflow save' or a template. 'doctor'
  lists a file that appeared on its own with its profile, nodes and sha; adopting it (the exact
  'doctor fix workflows.publications.adopt:...' it prints) creates it inactive and unapproved.

SCHEMA
  {
    "id":          "my-workflow-v1",       // must match filename
    "name":        "My Workflow",
    "description": "What it does",
    "version":     1,
    "is_active":   true,
    "nodes":       [...],
    "connections": [...]
  }

NODE OBJECT
  {
    "id":       "n1",                    // unique within workflow
    "type":     "system.rss_read",       // node type (monoagentcli ref nodes)
    "name":     "DW Feed",               // display name
    "position": { "x": 100, "y": 300 }, // canvas position (visual only)
    "config":   { ... }                  // node-specific config
  }

CONNECTION OBJECT
  {
    "id":            "c1",
    "source":        "n1",      // source node id
    "source_handle": "main",    // output handle (usually "main")
    "target":        "n2",      // target node id
    "target_handle": "main"     // input handle (usually "main")
  }

TRIGGER NODE
  trigger.schedule uses 6-field cron (sec min hour day month weekday):
  "cron": "0 0 9 * * *"   // every day at 09:00
  Activate the workflow, then run: monoagentcli daemon

PARALLEL BRANCHES
  Connect one source to multiple targets — all run concurrently.
  Rejoin branches with core.merge.

EXAMPLE — minimal two-node workflow:
  {
    "id": "hello-v1",
    "name": "Hello",
    "version": 1,
    "is_active": true,
    "nodes": [
      { "id":"n1", "type":"trigger.schedule", "name":"Daily",
        "position":{"x":100,"y":100},
        "config":{ "cron":"0 0 9 * * *" } },
      { "id":"n2", "type":"system.execute_command", "name":"Say hi",
        "position":{"x":350,"y":100},
        "config":{ "command":"echo hello" } }
    ],
    "connections": [
      { "id":"c1","source":"n1","source_handle":"main","target":"n2","target_handle":"main" }
    ]
  }

Import it: monoagentcli --profile <profile> workflow import --file hello-v1.json
Run: monoagentcli workflow run hello-v1

`)
		},
	}
}
