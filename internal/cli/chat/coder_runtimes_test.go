package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// allRuntimeCaps is a monomind that runs every full_access runtime in coder
// mode and takes --effort.
var allRuntimeCaps = append(append([]string{}, monomind.CoderCapabilities...), monomind.CapAgentExecFullAccessAny, monomind.CapAgentExecEffort)

func TestCoderStatusListsRuntimes(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	codexTarget := "codex"
	scan := []monomind.ScanEntry{
		{ID: "claude", Installed: true, FullAccess: true, ToolActivityFidelity: "full"},
		{ID: "codex", Installed: true, FullAccess: true, ToolActivityFidelity: "full", Resume: true, Effort: true, InitTarget: &codexTarget},
		{ID: "grok", Installed: false, FullAccess: true, ToolActivityFidelity: "start-only"},
		{ID: "vercel", Installed: true},
	}
	status := func() (map[string]any, map[string]map[string]any) {
		out, code := runCoderCLI(t, dbPath, "status")
		if code != 0 {
			t.Fatalf("status: exit %d %s", code, out)
		}
		var raw map[string]any
		decodeChatJSON(t, out, &raw)
		byID := map[string]map[string]any{}
		list, _ := raw["runtimes"].([]any)
		for _, r := range list {
			m := r.(map[string]any)
			byID[m["id"].(string)] = m
		}
		return raw, byID
	}

	// An older monomind (no agent-exec-full-access-any): claude alone, with
	// what that monomind supported filled in.
	withCoderCaps(t, monomind.CoderCapabilities...)
	withCoderScan(t, scan...)
	raw, rts := status()
	if raw["runtime"] != "claude" || raw["ready"] != true || len(rts) != 4 {
		t.Fatalf("old monomind status = %v", raw)
	}
	want := map[string]any{"id": "claude", "installed": true, "fullAccess": true, "ready": true, "toolActivity": "full",
		"resume": true, "effort": true, "maxTurns": true, "reportsCost": true, "initTarget": "claude"}
	for k, v := range want {
		if rts["claude"][k] != v {
			t.Errorf("claude.%s = %v, want %v", k, rts["claude"][k], v)
		}
	}
	if rts["codex"]["fullAccess"] != false || rts["codex"]["ready"] != false {
		t.Errorf("codex on an older monomind = %v", rts["codex"])
	}

	// The new contract: each runtime as scanned.
	withCoderCaps(t, allRuntimeCaps...)
	withCoderScan(t, scan...)
	_, rts = status()
	if c := rts["codex"]; c["ready"] != true || c["initTarget"] != "codex" || c["resume"] != true || c["maxTurns"] != false {
		t.Errorf("codex = %v", c)
	}
	if g := rts["grok"]; g["fullAccess"] != true || g["ready"] != false || g["toolActivity"] != "start-only" || g["initTarget"] != "" {
		t.Errorf("grok (not installed) = %v", g)
	}
	if v := rts["vercel"]; v["fullAccess"] != false || v["ready"] != false || v["toolActivity"] != "none" {
		t.Errorf("vercel = %v", v)
	}
	if c := rts["claude"]; c["resume"] != false {
		t.Errorf("new monomind's claude must be taken as scanned, got %v", c)
	}

	// No global caps: nothing is ready.
	withCoderCaps(t, monomind.CapAgentExecFullAccessAny)
	withCoderScan(t, scan...)
	if _, rts = status(); rts["codex"]["ready"] != false {
		t.Errorf("codex ready without the coder capabilities: %v", rts["codex"])
	}
}

const codexTranscript = `  echo '{"v":1,"type":"start","runtime":"codex","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"status","phase":"initializing"}'
  echo '{"v":1,"type":"status","phase":"settings","message":"Codex loads ~/.codex/config.toml and AGENTS.md"}'
  echo '{"v":1,"type":"session","session_id":"th_codex"}'
  echo '{"v":1,"type":"tool_activity","id":"c1","phase":"start","name":"command_execution","kind":"shell","input":{"command":"make test"}}'
  echo '{"v":1,"type":"tool_activity","id":"c1","phase":"end","name":"command_execution","ok":false,"output":"FAIL","exit_code":3}'
  echo '{"v":1,"type":"tool_activity","id":"c2","phase":"start","name":"command_execution","kind":"shell","input":{"command":"true"}}'
  echo '{"v":1,"type":"tool_activity","id":"c2","phase":"end","name":"command_execution","ok":true,"output":"","exit_code":0}'
  echo '{"v":1,"type":"tool_activity","id":"e1","phase":"start","name":"file_change","kind":"edit","input":{"file_path":"old.txt","old_string":"x","new_string":"y"}}'
  echo '{"v":1,"type":"tool_activity","id":"e1","phase":"end","name":"file_change","ok":true}'
  echo '{"v":1,"type":"tool_activity","id":"p1","phase":"start","name":"patch_apply","kind":"patch","input":{"files":[{"file_path":"a.go","action":"add"}]}}'
  echo '{"v":1,"type":"tool_activity","id":"p1","phase":"end","name":"patch_apply","ok":true}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"done"}'
  echo '{"v":1,"type":"done","exit_code":0}'`

func TestCoderTurnOnCodex(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "old.txt"), []byte("x"), 0o644)
	bin, argsLog := writeCoderMonomind(t, codexTranscript)
	withCoderCaps(t, allRuntimeCaps...)
	withCoderScan(t, monomind.ScanEntry{ID: "codex", Installed: true, FullAccess: true, ToolActivityFidelity: "full", Effort: true})
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, err := openTestChatStore(t, dbPath).CreateConversationModeEffort("default", "agent", "general", "codex", "", "gpt-6", ai.ModeCoder, cwd, "high")
	if err != nil {
		t.Fatal(err)
	}

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "fix it")
	if err != nil {
		t.Fatalf("codex turn: %v\n%s", err, out)
	}
	execLine := lastAgentExecLine(t, argsLog)
	for _, want := range []string{"--runtime codex", "--model gpt-6", "--effort high", "--access full", "--settings user,project,local", "--cwd " + cwd} {
		if !strings.Contains(execLine, want) {
			t.Errorf("exec argv lacks %q: %s", want, execLine)
		}
	}
	if strings.Contains(execLine, "CLAUDE_EFFORT") {
		t.Errorf("codex turn got the claude effort env: %s", execLine)
	}

	j := parseJournaledTurn(t, out)
	var notices []string
	for _, n := range j.byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(n.Payload, &p)
		notices = append(notices, p.Message)
	}
	if len(notices) != 3 || notices[1] != "Starting Codex…" || !strings.Contains(notices[2], "config.toml") {
		t.Errorf("notices = %q", notices)
	}
	started := map[string]chatevents.ToolStartedPayload{}
	for _, e := range j.byType(chatevents.EventToolStarted) {
		var p chatevents.ToolStartedPayload
		json.Unmarshal(e.Payload, &p)
		started[p.CallID] = p
	}
	if started["c1"].Kind != "shell" || started["e1"].Kind != "edit" || started["p1"].Kind != "patch" || started["c1"].Name != "command_execution" {
		t.Errorf("kinds = %+v", started)
	}
	if fe := started["e1"].FileExisted; fe == nil || !*fe {
		t.Errorf("edit of an existing file: fileExisted = %v", fe)
	}
	if started["p1"].FileExisted != nil || started["c1"].FileExisted != nil {
		t.Errorf("fileExisted set for patch/shell: %+v", started)
	}
	codes := map[string]*int{}
	for _, e := range j.byType(chatevents.EventToolCompleted) {
		var p chatevents.ToolCompletedPayload
		json.Unmarshal(e.Payload, &p)
		codes[p.CallID] = p.ExitCode
	}
	if codes["c1"] == nil || *codes["c1"] != 3 || codes["c2"] == nil || *codes["c2"] != 0 || codes["e1"] != nil {
		t.Errorf("exit codes: c1=%v c2=%v e1=%v", codes["c1"], codes["c2"], codes["e1"])
	}
	if p := j.finished(t); p.Status != chatevents.StatusCompleted {
		t.Errorf("turn.finished = %+v", p)
	}
}

func TestCoderTurnStartOnlyCallsCloseUnknown(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, `  echo '{"v":1,"type":"start","runtime":"grok","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"tool_activity","id":"g1","phase":"start","name":"bash","kind":"shell","input":{"command":"ls"}}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
  echo '{"v":1,"type":"done","exit_code":0}'`)
	withCoderCaps(t, allRuntimeCaps...)
	withCoderScan(t, monomind.ScanEntry{ID: "grok", Installed: true, FullAccess: true, ToolActivityFidelity: "start-only"})
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "grok", "", "", ai.ModeCoder, t.TempDir())

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	if err != nil {
		t.Fatalf("grok turn: %v\n%s", err, out)
	}
	j := parseJournaledTurn(t, out)
	done := j.byType(chatevents.EventToolCompleted)
	if len(done) != 1 {
		t.Fatalf("want the start-only call closed, got %d tool.completed", len(done))
	}
	var p chatevents.ToolCompletedPayload
	json.Unmarshal(done[0].Payload, &p)
	if p.CallID != "g1" || p.Cancelled || p.OK != nil || !strings.Contains(string(done[0].Payload), `"ok":null`) {
		t.Errorf("start-only close = %s", done[0].Payload)
	}
}

func TestCoderTurnEffortFallsBackToClaudeEnv(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, coderTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...) // no agent-exec-effort
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationModeEffort("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir(), "max")
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go"); err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	execLine := lastAgentExecLine(t, argsLog)
	if !strings.Contains(execLine, "--env CLAUDE_EFFORT=max") || strings.Contains(execLine, "--effort") {
		t.Errorf("exec argv: %s", execLine)
	}
}

func TestCoderTurnRefusesRuntimeWithoutFullAccess(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, coderTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...) // runs claude only
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "codex", "", "", ai.ModeCoder, t.TempDir())
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	var ce *coderError
	if err == nil || !errors.As(err, &ce) || ce.code != "coder_runtime_unsupported" || !strings.Contains(err.Error(), "newer monomind") {
		t.Fatalf("want coder_runtime_unsupported, got %v\n%s", err, out)
	}
	if logged, _ := os.ReadFile(argsLog); strings.Contains(string(logged), "agent exec") {
		t.Error("a refused runtime still ran the agent")
	}
}

// lastAgentExecLine is the last `agent exec` argv the fake monomind logged.
func lastAgentExecLine(t *testing.T, argsLog string) string {
	t.Helper()
	logged, _ := os.ReadFile(argsLog)
	var line string
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "agent exec") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("agent exec never ran:\n%s", logged)
	}
	return line
}

// A coder folder for a runtime without an init target of its own (pi, dsh,
// grok, copilot, …) gets AGENTS.md alone: `monomind init --target agents`
// when the scan names that target, mono-agent's own AGENTS.md when it names
// none (an older monomind). Never Claude's setup.
func TestCoderFolderForAgentsMDRuntimesHasNoClaudeSetup(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	dir := t.TempDir()
	bin, argsLog := filepath.Join(dir, "monomind"), filepath.Join(dir, "args.log")
	// Writes what each target would: AGENTS.md for agents, Claude's files
	// for claude.
	script := "#!/bin/sh\n" + `echo "$*" >> '` + argsLog + "'\n" +
		`if [ "$1" = "init" ]; then
  case "$*" in
    *"--target agents"*) echo x > "$3/AGENTS.md"; echo '{"root":"x","created":["AGENTS.md"],"skipped":[]}' ;;
    *) echo x > "$3/CLAUDE.md"; echo '{}' > "$3/.mcp.json"; mkdir -p "$3/.claude"; echo '{"root":"x","created":["CLAUDE.md"],"skipped":[]}' ;;
  esac
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	withCoderCaps(t, allRuntimeCaps...)
	agents := "agents"
	withCoderScan(t,
		monomind.ScanEntry{ID: "pi", Installed: true, FullAccess: true, InitTarget: &agents},
		monomind.ScanEntry{ID: "dsh", Installed: true, FullAccess: true, InitTarget: &agents},
		monomind.ScanEntry{ID: "copilot", Installed: true, FullAccess: true, InitTarget: &agents},
		monomind.ScanEntry{ID: "grok", Installed: true, FullAccess: true}) // older monomind: no target
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})

	for _, rt := range []string{"pi", "dsh", "copilot", "grok"} {
		out, code := runCoderCLI(t, dbPath, "workspace", "new", "--runtime", rt, "--root", filepath.Join(t.TempDir(), rt))
		var ws coderWorkspace
		decodeChatJSON(t, out, &ws)
		if code != 0 || ws.Init == nil || len(ws.Init.Created) != 1 || ws.Init.Created[0] != "AGENTS.md" {
			t.Fatalf("%s: exit %d %+v", rt, code, ws)
		}
		entries, _ := os.ReadDir(ws.Path)
		for _, e := range entries {
			if e.Name() != "AGENTS.md" && e.Name() != ".git" {
				t.Errorf("%s folder has %s; want AGENTS.md alone", rt, e.Name())
			}
		}
		if _, err := os.Stat(filepath.Join(ws.Path, "AGENTS.md")); err != nil {
			t.Errorf("%s folder has no AGENTS.md", rt)
		}
	}
	logged, _ := os.ReadFile(argsLog)
	if strings.Contains(string(logged), "--target claude") || strings.Count(string(logged), "--target agents") != 3 {
		t.Errorf("init calls:\n%s", logged)
	}
}
