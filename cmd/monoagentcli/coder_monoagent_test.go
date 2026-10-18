package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/monomind"
)

// minimalCoderTranscript is the shortest `agent exec` output that completes a
// coder turn and stores its session, so the next turn can resume it.
const minimalCoderTranscript = `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"session","session_id":"th_guidance"}'
  echo '{"v":1,"type":"assistant","text":"done"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"done"}'
  echo '{"v":1,"type":"done","exit_code":0}'`

// copyingSystemFile is a fake monomind `agent exec` body that copies the
// --system-file the turn passed to dst, which Exec deletes once the turn
// ends, and then plays transcript.
func copyingSystemFile(dst, transcript string) string {
	return `  prev=''
  for a in "$@"; do
    if [ "$prev" = "--system-file" ]; then cp "$a" '` + dst + `'; fi
    prev="$a"
  done
` + transcript
}

// Every coder turn tells the agent how to reach this monoagent installation,
// a resumed one included: the GUI runs each message as its own `chat`
// process, and a resumed session keeps the system prompt of its first turn
// only if the runtime says so. Dropping it from either leaves the agent
// building standalone code with no idea the nodes and workflows exist.
func TestCoderTurnTellsTheAgentHowToReachMonoagent(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	captured := filepath.Join(t.TempDir(), "system.md")
	bin, argsLog := writeCoderMonomind(t, copyingSystemFile(captured, minimalCoderTranscript))
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	cwd := t.TempDir()
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	for _, turn := range []string{"turn-1", "turn-2"} {
		os.Remove(captured)
		if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", turn, "--instance", "i", "--", "post my RSS digest to Slack every morning"); err != nil {
			t.Fatalf("%s: %v\n%s", turn, err, out)
		}
		raw, err := os.ReadFile(captured)
		if err != nil {
			t.Fatalf("%s: the agent exec was given no --system-file: %v", turn, err)
		}
		prompt := string(raw)
		for _, want := range []string{
			"working in " + cwd,               // the coder prompt it already had
			shellQuote(self),                  // this binary, by absolute path: the GUI may not have it on PATH
			"--profile default",               // the turn's profile; the CLI ignores MONOAGENT_PROFILE
			"--db-path " + shellQuote(dbPath), // the turn's database, which isn't the default one
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: system prompt lacks %q:\n%s", turn, want, prompt)
			}
		}
	}

	// The second turn really was a resume of the first one's session, so the
	// check above covered the resumed path.
	var execs []string
	logged, _ := os.ReadFile(argsLog)
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "agent exec") {
			execs = append(execs, l)
		}
	}
	if len(execs) != 2 || strings.Contains(execs[0], "--resume") || !strings.Contains(execs[1], "--resume th_guidance") {
		t.Fatalf("want a fresh turn then a resume of th_guidance, got:\n%s", strings.Join(execs, "\n"))
	}
}

// The invocation names the binary by absolute path and pins the turn's
// profile and database: the agent's shell has none of the chat process's
// flags, and the CLI reads the active profile from settings when a call
// carries none, so a bare call would land in another profile or database.
func TestMonoagentInvocation(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  globalConfig
		want string
	}{
		{"profile on the default database", globalConfig{ProfileID: "work", DBPath: defaultDBPath}, "/opt/m/monoagentcli --profile work"},
		{"no resolved profile", globalConfig{DBPath: defaultDBPath}, "/opt/m/monoagentcli"},
		{"no database path at all", globalConfig{ProfileID: "work"}, "/opt/m/monoagentcli --profile work"},
		{"another database", globalConfig{ProfileID: "work", DBPath: "/data/alt.db"}, "/opt/m/monoagentcli --profile work --db-path /data/alt.db"},
		{"a home-relative database", globalConfig{ProfileID: "work", DBPath: "~/alt/m.db"}, "/opt/m/monoagentcli --profile work --db-path " + filepath.Join(home, "alt", "m.db")},
	} {
		if got := monoagentInvocation("/opt/m/monoagentcli", &tc.cfg); got != tc.want {
			t.Errorf("%s: invocation = %q, want %q", tc.name, got, tc.want)
		}
	}

	// os.Executable can fail, and an empty path must not reach the agent as ''.
	if got := monoagentInvocation("", &globalConfig{ProfileID: "work", DBPath: defaultDBPath}); got != "monoagentcli --profile work" {
		t.Errorf("no binary path: invocation = %q, want the bare name, found on PATH", got)
	}

	// A relative path would resolve against the agent's folder, not the
	// chat process's, and find another database.
	got := monoagentInvocation("/opt/m/monoagentcli", &globalConfig{ProfileID: "work", DBPath: "rel/m.db"})
	if _, db, ok := strings.Cut(got, "--db-path "); !ok || !filepath.IsAbs(db) {
		t.Errorf("a relative database path must be made absolute, got %q", got)
	}
}

// An app bundle's binary has spaces in its path, and so can the database:
// the invocation must reach the CLI as the same arguments through a shell.
func TestMonoagentInvocationSurvivesAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs the invocation through sh")
	}
	bundle := filepath.Join(t.TempDir(), "Mono Agent.app", "Contents")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	argv := filepath.Join(t.TempDir(), "argv")
	fake := filepath.Join(bundle, "monoagentcli")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argv + "'\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	inv := monoagentInvocation(fake, &globalConfig{ProfileID: "work", DBPath: "/data/my dbs/m.db"})
	if out, err := exec.Command("sh", "-c", inv+` --json workflow search "daily digest"`).CombinedOutput(); err != nil {
		t.Fatalf("running %q: %v\n%s", inv, err, out)
	}
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	want := []string{"--profile", "work", "--db-path", "/data/my dbs/m.db", "--json", "workflow", "search", "daily digest"}
	if !slices.Equal(got, want) {
		t.Errorf("the CLI received %q, want %q", got, want)
	}
}

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
	cmds := coderPromptCommands(coderSystemPrompt("/w", "monoagentcli"))
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
	allowed := []string{"workflow search", "workflow templates use", "workflow validate", "workflow import", "workflow run", "node schema", "publication register"}
	for _, c := range coderPromptCommands(coderSystemPrompt("/w", "monoagentcli")) {
		path := strings.Join(c.path, " ")
		if len(c.path) == 0 || c.path[0] != "ref" && !slices.Contains(allowed, path) {
			t.Errorf("`<cli> %s` is outside what a coder turn may be taught (ref, %s)", c.text, strings.Join(allowed, ", "))
		}
		if path == "workflow run" && !slices.Contains(c.flags, "dry-run") {
			t.Errorf("`<cli> %s` is a real run; the prompt may only show --dry-run", c.text)
		}
	}
}

// The prompt also says where the line is, so the agent doesn't cross it on
// its own; the commands above keep it from being pointed past it. Whether a
// model keeps to it only a live turn shows. `node run` executes a node for
// real without any workflow run, so the rule has to name it: a live turn used
// it freely, and `node run comm.slack` would post.
func TestCoderPromptStatesTheGuardrails(t *testing.T) {
	prompt := coderSystemPrompt("/w", "monoagentcli")
	for _, want := range []string{"messages or people records", "wait for a yes", "activating a workflow", "node run"} {
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
	prompt := coderSystemPrompt("/w", "monoagentcli")
	// The manual's example is active, and an active workflow's triggers fire
	// on their own once the daemon runs: no run command, no yes from the
	// user. So what the prompt says about is_active is part of the flow.
	state := regexp.MustCompile(`"is_active": (?:true|false)`).FindString(prompt)
	if state == "" {
		t.Fatal(`the prompt doesn't say how to store a workflow ("is_active": …), and the manual's example is active`)
	}
	dbPath := newChatCLITestDB(t)
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
