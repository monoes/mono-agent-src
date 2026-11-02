package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
)

// withCoderCaps stands in for a monomind that advertises caps, and whose
// scan finds claude and codex installed with only claude full-access (as
// monomind 2.18 reports them) unless withCoderScan says otherwise.
func withCoderCaps(t *testing.T, caps ...string) {
	t.Helper()
	old := capabilityProbe
	capabilityProbe = func(*cobra.Command) (*monomind.CapabilitySet, error) {
		return monomind.NewCapabilitySet("9.0.0", caps...), nil
	}
	t.Cleanup(func() { capabilityProbe = old })
	withCoderScan(t,
		monomind.ScanEntry{ID: "claude", Installed: true, FullAccess: true, ToolActivityFidelity: "full"},
		monomind.ScanEntry{ID: "codex", Installed: true, ToolActivityFidelity: "none"})
}

// withCoderScan stands in for `agent scan` finding entries.
func withCoderScan(t *testing.T, entries ...monomind.ScanEntry) {
	t.Helper()
	old := runtimeScan
	runtimeScan = func(context.Context) (*monomind.ScanResult, error) {
		return &monomind.ScanResult{V: 1, Agents: entries}, nil
	}
	t.Cleanup(func() { runtimeScan = old })
}

// writeCoderMonomind writes a fake monomind that answers the handshake,
// `init --json` and `agent exec` (with transcript), logging every argv.
func writeCoderMonomind(t *testing.T, transcript string) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	argsLog = filepath.Join(dir, "args.log")
	script := "#!/bin/sh\n" +
		`echo "$*" >> '` + argsLog + "'\n" +
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi` + "\n" +
		`if [ "$1" = "init" ]; then echo 'initializing…'; echo '{"root":"x","created":["CLAUDE.md"],"skipped":[]}'; exit 0; fi` + "\n" +
		`if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then` + "\n" + transcript + "\n  exit 0\nfi\n" +
		`echo "unsupported: $*" >&2` + "\nexit 2\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, argsLog
}

// runCoderCLI runs `coder <args>` with --json and returns stdout and the
// exit code its error maps to.
func runCoderCLI(t *testing.T, dbPath string, args ...string) (string, int) {
	t.Helper()
	cmd := newCoderCmd(testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true}))
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err := cmd.Execute()
	return out.String(), exitCodeFor(err)
}

func setCoderSettings(t *testing.T, dbPath string, s coderSettings) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := saveCoderSettings(db.DB, s); err != nil {
		t.Fatal(err)
	}
}

func TestCoderSettingsLifecycle(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)

	out, code := runCoderCLI(t, dbPath, "status")
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if code != 0 || st.Enabled || st.Ready || st.MaxTurns != 200 || st.Timeout != "60m" ||
		!strings.HasSuffix(st.WorkspaceRoot, "monoagent-coder") || len(st.MissingCapabilities) != 4 {
		t.Fatalf("default status (exit %d) = %+v", code, st)
	}
	if out, code := runCoderCLI(t, dbPath, "enable"); code != 3 || !strings.Contains(out, "yes-i-understand") {
		t.Fatalf("enable without confirmation: exit %d %s", code, out)
	}
	out, code = runCoderCLI(t, dbPath, "enable", "--yes-i-understand")
	decodeChatJSON(t, out, &st)
	if code != 0 || !st.Enabled {
		t.Fatalf("enable: exit %d %+v", code, st)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--max-turns", "0"); code != 3 {
		t.Errorf("--max-turns 0: exit %d, want 3", code)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--budget-usd", "-1"); code != 3 {
		t.Errorf("--budget-usd -1: exit %d, want 3", code)
	}
	out, code = runCoderCLI(t, dbPath, "set", "--timeout", "2h", "--budget-usd", "5", "--max-turns", "50", "--workspace-root", "~/elsewhere")
	decodeChatJSON(t, out, &st)
	home, _ := os.UserHomeDir()
	if code != 0 || st.Timeout != "2h0m0s" || st.BudgetUSD != 5 || st.MaxTurns != 50 || st.WorkspaceRoot != filepath.Join(home, "elsewhere") || !st.Enabled {
		t.Fatalf("set: exit %d %+v", code, st)
	}
	out, _ = runCoderCLI(t, dbPath, "disable")
	decodeChatJSON(t, out, &st)
	if st.Enabled || st.MaxTurns != 50 {
		t.Fatalf("disable kept the other settings? %+v", st)
	}
}

func TestCoderConversationGate(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	writeCoderMonomind(t, "")

	withCoderCaps(t, monomind.CoderCapabilities...)
	out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--new-workspace")
	if code != 3 || !strings.Contains(out, `"code":"coder_disabled"`) {
		t.Fatalf("disabled: exit %d %s", code, out)
	}

	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	withCoderCaps(t, monomind.CapAgentExecFullAccess)
	out, code = runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--new-workspace")
	if code != 1 || !strings.Contains(out, `"code":"needs_monomind_update"`) || !strings.Contains(out, "agent-exec-settings") {
		t.Fatalf("old monomind: exit %d %s", code, out)
	}
}

func TestCoderConversationFolders(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	_, argsLog := writeCoderMonomind(t, "")
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})

	out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--new-workspace")
	if code != 0 {
		t.Fatalf("new workspace: exit %d %s", code, out)
	}
	var rec ai.ConversationRecord
	decodeChatJSON(t, out, &rec)
	home, _ := os.UserHomeDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved // macOS: /var/... is /private/var/...
	}
	gotDir, _ := filepath.EvalSymlinks(filepath.Dir(rec.Cwd))
	if rec.Mode != ai.ModeCoder || gotDir != filepath.Join(home, "monoagent-coder") {
		t.Fatalf("conversation = %+v", rec)
	}
	if fi, err := os.Stat(rec.Cwd); err != nil || !fi.IsDir() {
		t.Fatalf("workspace folder missing: %v", err)
	}
	if _, err := exec.LookPath("git"); err == nil {
		if _, err := os.Stat(filepath.Join(rec.Cwd, ".git")); err != nil {
			t.Errorf("workspace not git-initialized: %v", err)
		}
	}
	logged, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(logged), "init --project "+rec.Cwd+" --if-missing --json --no-graph --target claude") {
		t.Errorf("init not run with --if-missing on the folder:\n%s", logged)
	}

	picked := t.TempDir()
	out, code = runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--cwd", picked)
	decodeChatJSON(t, out, &rec)
	real, _ := filepath.EvalSymlinks(picked)
	if code != 0 || rec.Cwd != real {
		t.Fatalf("--cwd: exit %d %+v", code, rec)
	}

	file := filepath.Join(picked, "not-a-dir")
	os.WriteFile(file, nil, 0o644)
	for name, args := range map[string][]string{
		"missing folder":   {"--mode", "coder", "--cwd", filepath.Join(picked, "nope")},
		"file, not folder": {"--mode", "coder", "--cwd", file},
		"both folders":     {"--mode", "coder", "--cwd", picked, "--new-workspace"},
		"no folder":        {"--mode", "coder"},
		"scoped + cwd":     {"--access", "scoped", "--cwd", picked},
		"unknown mode":     {"--mode", "wizard"},
	} {
		if out, code := runChatHistory(t, dbPath, "default", append([]string{"create", "--runtime", "claude"}, args...)...); code != 3 {
			t.Errorf("%s: exit %d, want 3: %s", name, code, out)
		}
	}
	// This monomind runs only claude with full access (see withCoderCaps).
	if out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "codex", "--mode", "coder", "--cwd", picked); code != 3 || !strings.Contains(out, `"code":"coder_runtime_unsupported"`) {
		t.Errorf("codex runtime on an older monomind: exit %d, want 3: %s", code, out)
	}
	// One that runs codex with full access: accepted, set up for codex,
	// and the effort kept on the conversation.
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessAny)...)
	codexTarget := "codex"
	withCoderScan(t, monomind.ScanEntry{ID: "codex", Installed: true, FullAccess: true, ToolActivityFidelity: "full", InitTarget: &codexTarget})
	out, code = runChatHistory(t, dbPath, "default", "create", "--runtime", "codex", "--mode", "coder", "--cwd", picked, "--effort", "high")
	decodeChatJSON(t, out, &rec)
	if code != 0 || rec.RuntimeID != "codex" || rec.Effort != "high" || rec.Cwd != real {
		t.Fatalf("codex with full access: exit %d %+v", code, rec)
	}
	if logged, _ := os.ReadFile(argsLog); !strings.Contains(string(logged), "init --project "+real+" --if-missing --json --no-graph --target codex") {
		t.Errorf("codex folder not set up with --target codex:\n%s", logged)
	}
	if out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "vercel", "--mode", "coder", "--cwd", picked); code != 3 || !strings.Contains(out, "coder_runtime_unsupported") {
		t.Errorf("unknown runtime: exit %d %s", code, out)
	}

	out, _ = runCoderCLI(t, dbPath, "workspace", "list")
	var list []struct {
		Path          string `json:"path"`
		Conversations int    `json:"conversations"`
		Exists        bool   `json:"exists"`
	}
	decodeChatJSON(t, out, &list)
	if len(list) != 2 {
		t.Fatalf("workspace list = %+v", list)
	}
	found := false
	for _, workspace := range list {
		if !workspace.Exists {
			t.Errorf("missing workspace: %+v", workspace)
		}
		if workspace.Path == real && workspace.Conversations == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("workspace list missing picked folder with both conversations: %+v", list)
	}
}

const coderTranscript = `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"status","phase":"initializing","mcp_servers":[{"name":"monomind","status":"pending"}]}'
  echo '{"v":1,"type":"status","phase":"ready","mcp_servers":[{"name":"monomind","status":"failed"}]}'
  echo '{"v":1,"type":"session","session_id":"th_coder"}'
  echo '{"v":1,"type":"tool_activity","id":"t1","phase":"start","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}'
  echo '{"v":1,"type":"tool_activity","id":"t1","phase":"end","name":"Bash","ok":true,"output":"ok","duration_ms":812}'
  echo '{"v":1,"type":"tool_activity","id":"t2","phase":"start","name":"Edit","parent_tool_use_id":"t0","input":{"file_path":"/w/a.go","old_string":"x","new_string":"BIG"}}'
  echo '{"v":1,"type":"tool_activity","id":"t2","phase":"end","name":"Edit","denied":true,"output":"no"}'
  echo '{"v":1,"type":"assistant","text":"done"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"done"}'
  echo '{"v":1,"type":"done","exit_code":0,"background_pids":[4242]}'`

func TestCoderTurnRunsWithFullAccessAndJournalsToolActivity(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	big := strings.Repeat("y", chatevents.MaxToolPreviewBytes)
	// A process the turn left running, still alive when the turn ends.
	left := startBackground(t, "sleep", "60")
	transcript := strings.Replace(coderTranscript, "BIG", big, 1)
	transcript = strings.Replace(transcript, `"background_pids":[4242]`, `"background_pids":[`+strconv.Itoa(left)+`]`, 1)
	bin, argsLog := writeCoderMonomind(t, transcript)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, BudgetUSD: 3})
	cwd := t.TempDir()
	conv, err := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}

	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "t-tools", "--tools", "monoagent", "--", "hi"); exitCodeFor(err) != 3 {
		t.Fatalf("--tools on a coder conversation: %v\n%s", err, out)
	}

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--instance", "i", "--", "fix the tests")
	if err != nil {
		t.Fatalf("coder turn: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(argsLog)
	var execLine string
	for _, l := range strings.Split(string(logged), "\n") {
		if strings.HasPrefix(l, "agent exec") {
			execLine = l
		}
	}
	for _, want := range []string{"--runtime claude", "--cwd " + cwd, "--access full", "--settings user,project,local", "--max-turns 200", "--timeout 3600s", "--budget-usd 3", "--tools none", "--system-file"} {
		if !strings.Contains(execLine, want) {
			t.Errorf("exec argv lacks %q: %s", want, execLine)
		}
	}
	for _, never := range []string{"--allow-bash-prefix", "--tools-file", "MONOMIND_CWD"} {
		if strings.Contains(execLine, never) {
			t.Errorf("exec argv has %q: %s", never, execLine)
		}
	}

	j := parseJournaledTurn(t, out)
	var notices []chatevents.NoticePayload
	for _, n := range j.byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(n.Payload, &p)
		notices = append(notices, p)
	}
	wantNotices := []string{
		noticeCoderWorkspace + ": Working in " + cwd,
		noticeCoderStatus + ": Starting Claude Code… loading MCP servers (1)",
		noticeCoderStatus + ": Ready. Not available: monomind (failed)",
		noticeCoderBackground + ": 1 process started during this turn still running: " + strconv.Itoa(left) + " (sleep 60)",
	}
	if len(notices) != len(wantNotices) {
		t.Fatalf("notices = %+v", notices)
	}
	for i, n := range notices {
		if got := n.Code + ": " + n.Message; got != wantNotices[i] {
			t.Errorf("notice %d = %q, want %q", i, got, wantNotices[i])
		}
	}

	var started []chatevents.ToolStartedPayload
	for _, e := range j.byType(chatevents.EventToolStarted) {
		var p chatevents.ToolStartedPayload
		json.Unmarshal(e.Payload, &p)
		started = append(started, p)
	}
	if len(started) != 2 || !started[0].Native || started[0].Name != "Bash" || started[1].ParentCallID != "t0" {
		t.Fatalf("tool.started = %+v", started)
	}
	var edit map[string]string
	if err := json.Unmarshal(started[1].Arguments, &edit); err != nil || edit["file_path"] != "/w/a.go" || len(edit["new_string"]) > chatevents.MaxToolFieldBytes {
		t.Errorf("big Edit input lost its shape or wasn't bounded: %v %.80s", err, started[1].Arguments)
	}
	var done []chatevents.ToolCompletedPayload
	for _, e := range j.byType(chatevents.EventToolCompleted) {
		var p chatevents.ToolCompletedPayload
		json.Unmarshal(e.Payload, &p)
		done = append(done, p)
	}
	if len(done) != 2 || *done[0].OK != true || done[0].DurationMs != 812 || !done[1].Denied || *done[1].OK != false {
		t.Errorf("tool.completed = %+v", done)
	}
	if p := j.finished(t); p.Status != chatevents.StatusCompleted {
		t.Errorf("turn.finished = %+v", p)
	}
}

func TestCoderTurnRefusedOnceDisabled(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, coderTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...)
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "hi")
	var ce *coderError
	if err == nil || !errors.As(err, &ce) || ce.code != "coder_disabled" {
		t.Fatalf("want coder_disabled, got %v\n%s", err, out)
	}
	if p := parseJournaledTurn(t, out).finished(t); p.Status != chatevents.StatusFailed {
		t.Errorf("turn.finished = %+v", p)
	}
	if logged, _ := os.ReadFile(argsLog); strings.Contains(string(logged), "agent exec") {
		t.Error("a disabled coder turn still ran the agent")
	}
}

func TestCoderRootIsOneSharedFolder(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	_, argsLog := writeCoderMonomind(t, "")
	withCoderCaps(t, monomind.CoderCapabilities...)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "coder root")
	setCoderSettings(t, dbPath, coderSettings{Enabled: true, WorkspaceRoot: root})

	out, code := runCoderCLI(t, dbPath, "workspace", "root")
	var ws coderWorkspace
	decodeChatJSON(t, out, &ws)
	if code != 0 || ws.Path != root || !ws.Created || ws.Init == nil {
		t.Fatalf("workspace root: exit %d %+v", code, ws)
	}
	if _, err := exec.LookPath("git"); err == nil && !ws.Git {
		t.Error("root not git-initialized")
	}
	os.WriteFile(filepath.Join(root, "keep.txt"), []byte("mine"), 0o644)

	var convs []ai.ConversationRecord
	for i := 0; i < 2; i++ {
		out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--coder-root")
		var rec ai.ConversationRecord
		decodeChatJSON(t, out, &rec)
		if code != 0 || rec.Cwd != root {
			t.Fatalf("create --coder-root: exit %d %+v", code, rec)
		}
		convs = append(convs, rec)
	}
	if convs[0].ID == convs[1].ID {
		t.Error("two chats in the root should be two conversations")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "keep.txt")); string(b) != "mine" {
		t.Error("existing file in the root changed")
	}
	entries, _ := os.ReadDir(filepath.Dir(root))
	if len(entries) != 1 {
		t.Errorf("--coder-root must not create folders beside or inside the root: %v", entries)
	}
	if logged, _ := os.ReadFile(argsLog); strings.Count(string(logged), "init --project "+root+" --if-missing") != 3 {
		t.Errorf("init not run on the root each time:\n%s", logged)
	}
	if out, code := runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--coder-root", "--new-workspace"); code != 3 {
		t.Errorf("--coder-root with --new-workspace: exit %d %s", code, out)
	}
}

// startBackground starts a process as a turn would leave one behind and
// returns its pid; cleanup kills and reaps it.
func startBackground(t *testing.T, name string, args ...string) int {
	t.Helper()
	c := exec.Command(name, args...)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { c.Wait(); close(reaped) }()
	t.Cleanup(func() { c.Process.Kill(); <-reaped })
	return c.Process.Pid
}

// A monomind that cannot be run is not an outdated one: the probe failure
// is reported on its own and nothing is listed as missing.
func TestCoderStatusProbeFailure(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	old := capabilityProbe
	capabilityProbe = func(*cobra.Command) (*monomind.CapabilitySet, error) {
		return nil, errors.New("monomind: command not found\n  (mise shim)")
	}
	t.Cleanup(func() { capabilityProbe = old })
	withCoderScan(t)

	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 {
		t.Fatalf("status: exit %d %s", code, out)
	}
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if st.Ready || st.ProbeError != "monomind: command not found (mise shim)" || len(st.MissingCapabilities) != 0 || st.MonomindVersion != "" {
		t.Fatalf("probe failure: %+v", st)
	}
	if !strings.Contains(out, `"missingCapabilities":[]`) {
		t.Fatalf("missingCapabilities must stay an empty list: %s", out)
	}

	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	out, code = runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--new-workspace")
	if code != 1 || !strings.Contains(out, `"code":"monomind_unusable"`) || strings.Contains(out, "needs_monomind_update") {
		t.Fatalf("create: exit %d %s", code, out)
	}
}

// A long probe failure is cut.
func TestProbeErrorTextIsBounded(t *testing.T) {
	got := probeErrorText(errors.New(strings.Repeat("x", 1000)))
	if n := len([]rune(got)); n != maxProbeErrorLen+1 {
		t.Fatalf("length %d", n)
	}
}

// A monomind that runs but lacks capabilities is still an outdated one:
// what is missing and the version found are reported, no probe error.
func TestCoderStatusOutdatedMonomind(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t, monomind.CapAgentExecFullAccess)
	out, code := runCoderCLI(t, dbPath, "status")
	if code != 0 {
		t.Fatalf("status: exit %d %s", code, out)
	}
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if st.Ready || st.ProbeError != "" || st.MonomindVersion != "9.0.0" || len(st.MissingCapabilities) == 0 {
		t.Fatalf("outdated monomind: %+v", st)
	}
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	out, _ = runChatHistory(t, dbPath, "default", "create", "--runtime", "claude", "--mode", "coder", "--new-workspace")
	if !strings.Contains(out, "found 9.0.0") {
		t.Fatalf("message should say what was found: %s", out)
	}
}
