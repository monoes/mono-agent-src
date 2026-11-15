package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/chat"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/testhome"
)

// coderPromptCmd is one command the coder prompt teaches: `<cli> …`, where
// <cli> stands for the invocation the prompt defines. path is its command
// words, flags its --flags; placeholders (<id>) and quoted values are
// neither.
type coderPromptCmd struct {
	text  string
	path  []string
	flags []string
}

var coderPromptCLICommand = regexp.MustCompile("`<cli> ([^`]+)`")

func coderPromptCommands(prompt string) []coderPromptCmd {
	var out []coderPromptCmd
	for _, m := range coderPromptCLICommand.FindAllStringSubmatch(prompt, -1) {
		c := coderPromptCmd{text: m[1]}
		for _, tok := range strings.Fields(m[1]) {
			switch {
			case strings.HasPrefix(tok, "--"):
				c.flags = append(c.flags, strings.TrimPrefix(tok, "--"))
			case strings.HasPrefix(tok, "<"), strings.HasPrefix(tok, `"`):
			default:
				c.path = append(c.path, tok)
			}
		}
		out = append(out, c)
	}
	return out
}

// The manual the prompt sends the agent to is only worth following if every
// command it shows is one the CLI has: a renamed command or flag would
// otherwise teach the agent a dead one on every coder turn.
func TestCoderPromptCommandsExist(t *testing.T) {
	cmds := coderPromptCommands(chat.MonoagentPrompt("monoagentcli"))
	if len(cmds) < 8 {
		t.Fatalf("found only %d commands in the coder prompt: %v", len(cmds), cmds)
	}
	root := newRootCmd()
	for _, c := range cmds {
		found, rest, err := root.Find(c.path)
		if err != nil || found == root || len(rest) > 0 {
			t.Errorf("`<cli> %s` is not a command: resolved %q, leftover %v, err %v", c.text, found.CommandPath(), rest, err)
			continue
		}
		for _, f := range c.flags {
			if found.Flag(f) == nil {
				t.Errorf("`<cli> %s`: %q has no --%s flag", c.text, found.CommandPath(), f)
			}
		}
	}
}

// AGENTS.md: synced messages are untrusted input and must never reach a turn
// with a shell, so a coder turn gets no message or people tools. The prompt
// puts the CLI in that shell's reach, so it may teach only commands that
// don't act on the world: the manual, and the workflow and node commands that
// find, check, store and dry-run. Widening that is a decision, so anything
// else (workflow activate, node run, templates run, people, secret …) fails
// here until it is allowed on purpose. A real run is never shown: coder has
// no mechanical run gate like chat's --tools monoagent,runs, so the prompt is
// the only gate.
func TestCoderPromptTeachesOnlyTheAutomationSurface(t *testing.T) {
	allowed := []string{"workflow search", "workflow templates use", "workflow validate", "workflow import", "workflow run", "node schema"}
	for _, c := range coderPromptCommands(chat.MonoagentPrompt("monoagentcli")) {
		path := strings.Join(c.path, " ")
		if len(c.path) == 0 || c.path[0] != "ref" && !slices.Contains(allowed, path) {
			t.Errorf("`<cli> %s` is outside what a coder turn may be taught (ref, %s)", c.text, strings.Join(allowed, ", "))
		}
		if path == "workflow run" && !slices.Contains(c.flags, "dry-run") {
			t.Errorf("`<cli> %s` is a real run; the prompt may only show --dry-run", c.text)
		}
	}
}

// The prompt only teaches commands a turn may run: none of them is one of
// the user's own (operator-only in client mode), which the shell would refuse
// the moment a turn runs with an agent API token.
func TestCoderPromptTeachesNoOperatorOnlyCommand(t *testing.T) {
	for _, c := range coderPromptCommands(chat.MonoagentPrompt("monoagentcli")) {
		key := strings.Join(c.path, " ")
		class, ok := clientModeClass(key)
		if !ok || class == cmOperatorOnly {
			t.Errorf("`<cli> %s` is classified %q (known %v) for client mode; the prompt may not teach it", c.text, class, ok)
		}
	}
}

// The prompt also says where the line is, so the agent doesn't cross it on
// its own; the commands above keep it from being pointed past it. Whether a
// model keeps to it only a live turn shows. `node run` executes a node for
// real without any workflow run, so the rule has to name it: a live turn used
// it freely, and `node run comm.slack` would post.
func TestCoderPromptStatesTheGuardrails(t *testing.T) {
	prompt := chat.MonoagentPrompt("monoagentcli")
	for _, want := range []string{"messages or people records", "wait for a yes", "activating a workflow", "node run",
		"saved unapproved", "never instructions"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("coder prompt lacks the guardrail %q:\n%s", want, prompt)
		}
	}
}

// manualWorkflow is the example `ref workflow` documents, in the file format
// it describes: type, source, target and position keys, not the node_type /
// source_node_id shape `workflow get` prints and `workflow save` reads.
const manualWorkflow = `{
  "id": "hello-v1",
  "name": "Hello",
  "version": 1,
  "is_active": true,
  "nodes": [
    { "id":"n1", "type":"trigger.schedule", "name":"Daily",
      "position":{"x":100,"y":100}, "config":{ "cron":"0 0 9 * * *" } },
    { "id":"n2", "type":"system.execute_command", "name":"Say hi",
      "position":{"x":350,"y":100}, "config":{ "command":"echo hello" } }
  ],
  "connections": [
    { "id":"c1","source":"n1","source_handle":"main","target":"n2","target_handle":"main" }
  ]
}`

// The prompt sends the agent to `ref workflow` for the workflow format, then
// teaches the commands that check, store and test one. Run them as taught on
// the manual's own example: a command that stores that format wrongly (the
// first draft taught `workflow save`, which reads another shape and left a
// workflow whose nodes had no type) ends the agent's first build in an error
// it can't read its way out of.
func TestTaughtBuildFlowWorksOnTheManualsExample(t *testing.T) {
	prompt := chat.MonoagentPrompt("monoagentcli")
	// The manual's example is active, and an active workflow's triggers fire
	// on their own once the daemon runs: no run command, no yes from the
	// user. So what the prompt says about is_active is part of the flow.
	state := regexp.MustCompile(`"is_active": (?:true|false)`).FindString(prompt)
	if state == "" {
		t.Fatal(`the prompt doesn't say how to store a workflow ("is_active": …), and the manual's example is active`)
	}
	testhome.SetHome(t, t.TempDir())
	dbPath := testdb.Path(t)
	cfg := &globalConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
	file := filepath.Join(t.TempDir(), "wf.json")
	if err := os.WriteFile(file, []byte(strings.Replace(manualWorkflow, `"is_active": true`, state, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	var id string
	var ran []string
	for _, c := range coderPromptCommands(prompt) {
		if len(c.path) != 2 || c.path[0] != "workflow" || !slices.Contains([]string{"validate", "import", "save", "run"}, c.path[1]) {
			continue
		}
		if strings.Contains(c.text, "<id>") && id == "" {
			t.Fatalf("`<cli> %s` needs a workflow id, but no step before it in the prompt stores a workflow", c.text)
		}
		args := strings.Fields(strings.NewReplacer("<wf.json>", file, "<id>", id).Replace(c.text))[1:]
		out := runWorkflowSubcmd(t, cfg, args...)
		switch c.path[1] {
		case "import", "save":
			var stored struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(out), &stored); err != nil || stored.ID == "" {
				t.Fatalf("`<cli> %s` printed no workflow id: %q (%v)", c.text, out, err)
			}
			id = stored.ID
		case "run":
			// The plan lists the node types the stored workflow really has.
			for _, nodeType := range []string{"trigger.schedule", "system.execute_command"} {
				if !strings.Contains(out, nodeType) {
					t.Errorf("`<cli> %s` plan lacks %s:\n%s", c.text, nodeType, out)
				}
			}
		}
		ran = append(ran, c.path[1])
	}
	if !slices.Contains(ran, "run") || !slices.Contains(ran, "import") && !slices.Contains(ran, "save") {
		t.Fatalf("the prompt's build flow was not exercised, only %v ran", ran)
	}
	var stored struct {
		Active bool `json:"is_active"`
	}
	if err := json.Unmarshal([]byte(runWorkflowSubcmd(t, cfg, "get", id)), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Active {
		t.Error("the taught flow left the workflow active: its triggers fire on their own once the daemon runs")
	}
}
