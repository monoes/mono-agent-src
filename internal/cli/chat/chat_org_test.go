package chat

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

// writeOrgMonomind is a fake monomind for a dynamic-org turn. The lead's
// exec calls org_spawn (waiting), or $ORG_LEAD_CALL and then
// $ORG_LEAD_CALL2 when set, and saves the tool results it gets back;
// a worker's exec (its system prompt names it a worker) edits a file and
// reports. Each copies its system prompt to lead-system.txt or
// worker-system.txt next to argsLog. `pick` ranks nothing and `org skills show` fails, so staffing
// uses the built-in roles.
func writeOrgMonomind(t *testing.T) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	argsLog = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
echo "$*" >> '` + argsLog + `'
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1" = "pick" ]; then echo '{"agents":{"confident":false,"ranked":[]},"skills":{"confident":false,"ranked":[]}}'; exit 0; fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  sys=""
  tools=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--system-file" ]; then sys="$a"; fi
    if [ "$prev" = "--tools-file" ]; then tools="$a"; fi
    prev="$a"
  done
  if [ -n "$sys" ] && grep -q "a worker in a team" "$sys"; then
    cp "$sys" '` + filepath.Join(dir, "worker-system.txt") + `'
    echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":2,"access":"read"}'
    echo '{"v":1,"type":"session","session_id":"th_worker"}'
    echo '{"v":1,"type":"assistant","text":"Checking the cache."}'
    echo '{"v":1,"type":"tool_activity","id":"r1","phase":"start","name":"Read","input":{"file_path":"/w/cache.go"}}'
    echo '{"v":1,"type":"tool_activity","id":"r1","phase":"end","name":"Read","ok":true,"output":"package cache"}'
    echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"The cache is in cache.go.","cost_usd":0.002}'
    echo '{"v":1,"type":"done","exit_code":0}'
    exit 0
  fi
  if [ -n "$sys" ]; then cp "$sys" '` + filepath.Join(dir, "lead-system.txt") + `'; fi
  if [ -n "$tools" ]; then cp "$tools" '` + filepath.Join(dir, "lead-tools.json") + `'; fi
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"session","session_id":"th_lead"}'
  if [ -n "$ORG_LEAD_CALL" ]; then
    echo "$ORG_LEAD_CALL"
  else
    echo '{"v":1,"type":"tool_call","id":"c1","name":"org_spawn","args":{"brief":"investigate where the cache lives","wait":true}}'
  fi
  read -r reply
  printf '%s\n' "$reply" > '` + filepath.Join(dir, "reply.json") + `'
  if [ -n "$ORG_LEAD_CALL2" ]; then
    echo "$ORG_LEAD_CALL2"
    read -r reply2
    printf '%s\n' "$reply2" > '` + filepath.Join(dir, "reply2.json") + `'
  fi
  echo '{"v":1,"type":"assistant","text":"The team found it."}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"The team found it."}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo "unsupported: $*" >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, argsLog
}

func orgScan(callerTools bool) monomind.ScanEntry {
	return monomind.ScanEntry{ID: "claude", Installed: true, FullAccess: true, ToolActivityFidelity: "full", Resume: true,
		AccessModes: []string{"scoped", "read", "full"}, CallerTools: true, CallerToolsWithFullAccess: callerTools}
}

func TestDynamicOrgTurnSpawnsAndJournalsWorkers(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "") // staffing must not reach Jev from a test
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	cwd := t.TempDir()
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, cwd)
	if err != nil {
		t.Fatal(err)
	}
	out, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic")
	var rec ai.ConversationRecord
	decodeChatJSON(t, out, &rec)
	if code != 0 || rec.OrgMode != ai.OrgModeDynamic {
		t.Fatalf("set-org: exit %d %s", code, out)
	}

	out, err = runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "where is the cache?")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(argsLog)
	var leadLine, workerLine string
	for _, l := range strings.Split(string(logged), "\n") {
		if !strings.HasPrefix(l, "agent exec") {
			continue
		}
		if strings.Contains(l, "--access full") {
			leadLine = l
		} else {
			workerLine = l
		}
	}
	for _, want := range []string{"--access full", "--tools stdio", "--tools-file", "--tool-timeout 130s"} {
		if !strings.Contains(leadLine, want) {
			t.Errorf("lead argv lacks %q: %s", want, leadLine)
		}
	}
	// The worker's only caller tool is ask_user (#256).
	for _, want := range []string{"--access read", "--cwd " + cwd, "--tools stdio", "--tools-file", "--system-file", "--model opus"} {
		if !strings.Contains(workerLine, want) {
			t.Errorf("worker argv lacks %q: %s", want, workerLine)
		}
	}

	reply, _ := os.ReadFile(filepath.Join(filepath.Dir(argsLog), "reply.json"))
	if !strings.Contains(string(reply), "The cache is in cache.go.") || !strings.Contains(string(reply), `\"status\":\"done\"`) {
		t.Errorf("the lead's org_spawn result = %s", reply)
	}

	j := parseJournaledTurn(t, out)
	var spawned chatevents.AgentSpawnedPayload
	if evs := j.byType(chatevents.EventAgentSpawned); len(evs) != 1 {
		t.Fatalf("agent.spawned events = %d", len(evs))
	} else {
		json.Unmarshal(evs[0].Payload, &spawned)
	}
	if spawned.AgentID != "w1" || spawned.Role != "Researcher" || spawned.Access != "research" || spawned.Runtime != "claude" || spawned.Fidelity != "full" {
		t.Errorf("spawned = %+v", spawned)
	}
	var finished chatevents.AgentFinishedPayload
	json.Unmarshal(j.byType(chatevents.EventAgentFinished)[0].Payload, &finished)
	if finished.Outcome != chatevents.AgentDone || finished.CostUSD == nil || *finished.CostUSD != 0.002 {
		t.Errorf("finished = %+v", finished)
	}
	workerTools := 0
	for _, e := range j.byType(chatevents.EventToolStarted) {
		var p chatevents.ToolStartedPayload
		json.Unmarshal(e.Payload, &p)
		if p.AgentID == "w1" && p.CallID == "w1:r1" {
			workerTools++
		}
	}
	if workerTools != 1 {
		t.Errorf("worker tool calls journaled = %d, want 1", workerTools)
	}
	if p := j.finished(t); p.Status != chatevents.StatusCompleted {
		t.Errorf("turn.finished = %+v", p)
	}
	// The worker's text and usage carry its agentId (#257, #258); the
	// lead's text doesn't.
	var texts []string
	for _, e := range j.byType(chatevents.EventAssistantDelta) {
		var p chatevents.AssistantDeltaPayload
		json.Unmarshal(e.Payload, &p)
		texts = append(texts, p.AgentID+"|"+p.PartID+"|"+p.Text)
	}
	if !slices.Contains(texts, "w1|w1:p1|Checking the cache.") || slices.ContainsFunc(texts, func(s string) bool { return strings.HasPrefix(s, "|w1") }) {
		t.Errorf("assistant.delta = %v", texts)
	}
	workerUsage := false
	for _, e := range j.byType(chatevents.EventUsageUpdated) {
		var p chatevents.UsageUpdatedPayload
		json.Unmarshal(e.Payload, &p)
		if p.AgentID == "w1" && p.CostUSD != nil && *p.CostUSD == 0.002 {
			workerUsage = true
		}
	}
	if !workerUsage {
		t.Error("no usage.updated for w1")
	}

	// chat history events --agent and transcript --by-agent.
	out, code = runChatHistory(t, dbPath, "default", "events", conv.ID, "turn-1", "--agent", "w1")
	var page chatEventPage
	decodeChatJSON(t, out, &page)
	if code != 0 || len(page.Items) == 0 {
		t.Fatalf("events --agent: exit %d %s", code, out)
	}
	for _, r := range page.Items {
		if eventAgentID(r) != "w1" {
			t.Errorf("events --agent w1 returned %s %s", r.Type, r.Payload)
		}
	}
	out, code = runChatHistory(t, dbPath, "default", "transcript", "--by-agent", conv.ID, "turn-1")
	var tr struct {
		Items []agentTranscript `json:"items"`
	}
	decodeChatJSON(t, out, &tr)
	if code != 0 || len(tr.Items) != 2 || tr.Items[0].AgentID != "lead" || tr.Items[1].AgentID != "w1" {
		t.Fatalf("transcript --by-agent: exit %d %s", code, out)
	}
	w := tr.Items[1]
	if w.Role != "Researcher" || w.Status != chatevents.AgentDone || w.Text != "Checking the cache." || w.Tools != 1 || len(w.Messages) != 2 || w.Messages[0].Direction != "brief" {
		t.Errorf("w1 transcript = %+v", w)
	}
	if !strings.Contains(tr.Items[0].Text, "The team found it.") {
		t.Errorf("lead transcript = %+v", tr.Items[0])
	}
	if _, code := runChatHistory(t, dbPath, "default", "transcript", "--by-agent", conv.ID); code == 0 {
		t.Error("--by-agent needs a conversation and a turn")
	}
}

func TestDynamicOrgFallsBackToSoloWithoutCallerTools(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, coderTranscript)
	withCoderCaps(t, monomind.CoderCapabilities...) // no agent-exec-full-access-tools
	withCoderScan(t, orgScan(false))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if err := store.SetConversationOrgMode(conv.ID, "default", ai.OrgModeDynamic); err != nil {
		t.Fatal(err)
	}
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "hi")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	logged, _ := os.ReadFile(argsLog)
	if strings.Contains(string(logged), "--tools-file") {
		t.Error("a lead without caller-tool support must run without org tools")
	}
	found := false
	for _, e := range parseJournaledTurn(t, out).byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(e.Payload, &p)
		if p.Code == noticeOrgUnavailable && strings.Contains(p.Message, "works alone") {
			found = true
		}
	}
	if !found {
		t.Error("the solo fallback must be explained in a notice")
	}
}

func TestSetOrgRefusesAssistantConversations(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, _ := openTestChatStore(t, dbPath).CreateConversation("default", "agent", "general", "claude", "", "")
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 3 {
		t.Errorf("assistant conversation: exit %d, want 3", code)
	}
	if _, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "crowd"); code != 3 {
		t.Errorf("bad mode: exit %d, want 3", code)
	}
}

func TestCoderSetOrgRouter(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	var st coderStatus
	out, _ := runCoderCLI(t, dbPath, "status")
	decodeChatJSON(t, out, &st)
	if st.OrgRouter != "auto" {
		t.Fatalf("default router = %q", st.OrgRouter)
	}
	out, code := runCoderCLI(t, dbPath, "set", "--org-router", "jev")
	decodeChatJSON(t, out, &st)
	if code != 0 || st.OrgRouter != "jev" {
		t.Fatalf("set router = exit %d %q", code, st.OrgRouter)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-router", "dice"); code != 3 {
		t.Errorf("bad router: exit %d", code)
	}
}

func TestCoderSetOrgSettings(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	withCoderCaps(t)
	out, code := runCoderCLI(t, dbPath, "status")
	var st coderStatus
	decodeChatJSON(t, out, &st)
	if code != 0 || st.OrgMaxAgents != 6 || st.OrgMaxConcurrent != 3 || st.OrgModelPicker != "lead-then-jev" || st.OrgWriters != "shared" {
		t.Fatalf("defaults = %+v", st)
	}
	out, code = runCoderCLI(t, dbPath, "set", "--org-max-agents", "4", "--org-max-concurrent", "2", "--org-budget-usd", "1.5", "--org-model-picker", "lead")
	decodeChatJSON(t, out, &st)
	if code != 0 || st.OrgMaxAgents != 4 || st.OrgMaxConcurrent != 2 || st.OrgBudgetUSD != 1.5 || st.OrgModelPicker != "lead" {
		t.Fatalf("set = exit %d %+v", code, st)
	}
	out, code = runCoderCLI(t, dbPath, "set", "--org-writers", "isolated")
	decodeChatJSON(t, out, &st)
	if code != 0 || st.OrgWriters != "isolated" || st.OrgModelPicker != "lead" {
		t.Fatalf("set writers = exit %d %+v", code, st)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-writers", "parallel"); code != 3 {
		t.Errorf("bad writers: exit %d", code)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-model-picker", "dice"); code != 3 {
		t.Errorf("bad picker: exit %d", code)
	}
	if _, code := runCoderCLI(t, dbPath, "set", "--org-max-agents", "0"); code != 3 {
		t.Errorf("zero agents: exit %d", code)
	}
}

// writeLeadEditMonomind is a fake monomind whose lead starts a writing
// worker, edits a file itself while the worker runs, then org_waits. The
// worker holds the write lease for about a second.
func writeLeadEditMonomind(t *testing.T) (bin, reply string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "monomind")
	reply = filepath.Join(dir, "wait-reply.json")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1" = "pick" ]; then echo '{"agents":{"confident":false,"ranked":[]},"skills":{"confident":false,"ranked":[]}}'; exit 0; fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  sys=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--system-file" ]; then sys="$a"; fi
    prev="$a"
  done
  if [ -n "$sys" ] && grep -q "a worker in a team" "$sys"; then
    echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":2,"access":"full"}'
    sleep 1
    echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"Fixed it.","cost_usd":0.002}'
    echo '{"v":1,"type":"done","exit_code":0}'
    exit 0
  fi
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"tool_call","id":"c1","name":"org_spawn","args":{"brief":"implement the fix","access":"coding"}}'
  read -r spawned
  sleep 0.3
  echo '{"v":1,"type":"tool_activity","id":"e1","phase":"start","name":"Edit","input":{"file_path":"/w/main.go"}}'
  echo '{"v":1,"type":"tool_activity","id":"e1","phase":"end","name":"Edit","ok":true}'
  echo '{"v":1,"type":"tool_call","id":"c2","name":"org_wait","args":{"timeout_s":10}}'
  read -r waited
  printf '%s\n' "$waited" > '` + reply + `'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"Done."}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
echo "unsupported: $*" >&2
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, reply
}

// #260 end to end: the lead's Edit event goes through the coder turn's
// onEvent chain into the conductor. Made while a writer holds the write
// lease, it is journaled as a warning notice and reported in the lead's
// next org tool result.
func TestDynamicOrgLeadEditWhileAWriterRunsIsReported(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, replyPath := writeLeadEditMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, code := runChatHistory(t, dbPath, "default", "set-org", conv.ID, "dynamic"); code != 0 {
		t.Fatalf("set-org: %s", out)
	}
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "fix the bug")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	j := parseJournaledTurn(t, out)
	conflict := false
	for _, e := range j.byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(e.Payload, &p)
		if p.Code == "org_lead_edit_conflict" && strings.Contains(p.Message, "/w/main.go") && strings.Contains(p.Message, "w1") {
			conflict = true
		}
	}
	if !conflict {
		t.Errorf("no org_lead_edit_conflict notice in the journal:\n%s", out)
	}
	reply, _ := os.ReadFile(replyPath)
	if !strings.Contains(string(reply), "warnings") || !strings.Contains(string(reply), "main.go") || !strings.Contains(string(reply), "Fixed it.") {
		t.Errorf("the lead's org_wait result = %s", reply)
	}
}

// writeAskingOrgMonomind: the lead spawns a worker (waiting); the worker
// asks the user a question with ask_user and reports the answer it got.
func writeAskingOrgMonomind(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "monomind")
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi
if [ "$1" = "pick" ]; then echo '{"agents":{"confident":false,"ranked":[]},"skills":{"confident":false,"ranked":[]}}'; exit 0; fi
if [ "$1" = "agent" ] && [ "$2" = "exec" ]; then
  sys=""
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--system-file" ]; then sys="$a"; fi
    prev="$a"
  done
  if [ -n "$sys" ] && grep -q "a worker in a team" "$sys"; then
    echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":2,"access":"full"}'
    echo '{"v":1,"type":"tool_call","id":"a1","name":"ask_user","args":{"question":"Which database?"}}'
    read -r reply
    printf '%s\n' "$reply" > '` + filepath.Join(dir, "worker-reply.json") + `'
    echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"Used the answer."}'
    echo '{"v":1,"type":"done","exit_code":0}'
    exit 0
  fi
  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"tool_call","id":"c1","name":"org_spawn","args":{"brief":"implement the storage layer","access":"coding","wait":true}}'
  read -r reply
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"Done."}'
  echo '{"v":1,"type":"done","exit_code":0}'
  exit 0
fi
exit 2
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	return bin, dir
}

func TestWorkerAsksTheUserAndGetsTheAnswer(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, dir := writeAskingOrgMonomind(t)
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err := store.SetConversationOrgMode(conv.ID, "default", ai.OrgModeDynamic); err != nil {
		t.Fatal(err)
	}

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-q", "--", "build it")
		done <- result{out, err}
	}()

	// Wait for the question in the journal, then answer it the way
	// `chat turn answer` does.
	var asked bool
	for i := 0; i < 300 && !asked; i++ {
		time.Sleep(20 * time.Millisecond)
		evs, _ := store.GetEvents(conv.ID, "turn-q", "default", 0, 500)
		for _, ev := range evs {
			var m chatevents.AgentMessagePayload
			if ev.Type == chatevents.EventAgentMessage && json.Unmarshal(ev.Payload, &m) == nil && m.Direction == "question" {
				asked = m.AgentID == "w1" && m.QuestionID == "q1" && m.Text == "Which database?"
			}
		}
	}
	if !asked {
		t.Fatal("the worker's question never reached the journal")
	}
	if err := checkOpenQuestion(store, "default", conv.ID, "turn-q", "w1", "q9"); err == nil {
		t.Error("an unknown question must be refused")
	}
	if err := checkOpenQuestion(store, "default", conv.ID, "turn-q", "w1", "q1"); err != nil {
		t.Fatalf("open question refused: %v", err)
	}
	if err := store.AddAnswer("default", conv.ID, "turn-q", "w1", "q1", "Postgres"); err != nil {
		t.Fatal(err)
	}

	var r result
	select {
	case r = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the turn did not finish after the answer")
	}
	if r.err != nil {
		t.Fatalf("turn: %v\n%s", r.err, r.out)
	}
	reply, _ := os.ReadFile(filepath.Join(dir, "worker-reply.json"))
	if !strings.Contains(string(reply), "The user answered: Postgres") {
		t.Errorf("the worker got %s", reply)
	}
	j := parseJournaledTurn(t, r.out)
	answered := false
	for _, e := range j.byType(chatevents.EventAgentMessage) {
		var m chatevents.AgentMessagePayload
		json.Unmarshal(e.Payload, &m)
		if m.Direction == "followup" && m.From == "user" && m.QuestionID == "q1" && m.Text == "Postgres" {
			answered = true
		}
	}
	if !answered {
		t.Error("the answer must be journaled as the worker's follow-up from the user")
	}
	if err := checkOpenQuestion(store, "default", conv.ID, "turn-q", "w1", "q1"); err == nil {
		t.Error("a finished turn's question must be refused")
	}
	if err := store.AddAnswer("default", conv.ID, "turn-q", "w1", "q1", "again"); err == nil {
		t.Error("a second answer must be refused")
	}
}

func TestChatHistoryAnswerRefusesBadInput(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if _, code := runChatTurn(t, dbPath, "answer", conv.ID, "nope", "--agent", "w1", "--question", "q1", "--text", "x"); code == 0 {
		t.Error("an unknown turn must be refused")
	}
	if _, code := runChatTurn(t, dbPath, "answer", conv.ID, "nope", "--agent", "w1"); code != 3 {
		t.Errorf("missing flags: exit %d, want 3", code)
	}
}

// runChatTurn runs `chat turn <args>` with --json.
func runChatTurn(t *testing.T, dbPath string, args ...string) (string, int) {
	t.Helper()
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	var err error
	out := captureStdout(t, func() {
		cmd := newChatCmd(cfg)
		cmd.SetArgs(append([]string{"turn"}, args...))
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err = cmd.Execute()
	})
	return out, exitCodeFor(err)
}

func TestChatTurnAnswerRefusesAClosedQuestion(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if _, _, err := store.CreateTurn(conv.ID, "default", "t1", "i", "go"); err != nil {
		t.Fatal(err)
	}
	q := chatevents.AgentMessagePayload{AgentID: "w1", Direction: "question", QuestionID: "q1", From: "w1", To: "user", Text: "Which DB?"}
	if _, err := store.AppendEvent("default", conv.ID, "t1", chatevents.EventAgentMessage, q); err != nil {
		t.Fatal(err)
	}
	if _, code := runChatTurn(t, dbPath, "answer", conv.ID, "t1", "--agent", "w1", "--question", "q1", "--text", "x"); code != 0 {
		t.Fatalf("open question: exit %d", code)
	}
	closedByTimeout := chatevents.AgentMessagePayload{AgentID: "w1", Direction: "followup", QuestionID: "q2", From: "system", Text: "No answer"}
	store.AppendEvent("default", conv.ID, "t1", chatevents.EventAgentMessage, chatevents.AgentMessagePayload{AgentID: "w1", Direction: "question", QuestionID: "q2", From: "w1", To: "user", Text: "?"})
	store.AppendEvent("default", conv.ID, "t1", chatevents.EventAgentMessage, closedByTimeout)
	if _, code := runChatTurn(t, dbPath, "answer", conv.ID, "t1", "--agent", "w1", "--question", "q2", "--text", "late"); code != 3 {
		t.Errorf("a timed-out question must be refused: exit %d", code)
	}
	if err := checkOpenQuestion(store, "default", conv.ID, "t1", "w1", "q2"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("refusal = %v", err)
	}
}

// When the ids of the conversation's stored workers cannot be read, the turn does not start a conductor: a new worker
// could reuse a stored one's id. The agent works alone and the notice says why; nothing is spawned.
func TestDynamicOrgDoesNotStartWhenTheStoredWorkerIDsCannotBeRead(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	dbPath := newChatCLITestDB(t)
	bin, argsLog := writeCoderMonomind(t, coderTranscript) // a lead without org tools makes no org call
	withCoderCaps(t, append(monomind.CoderCapabilities, monomind.CapAgentExecFullAccessTools, monomind.CapAgentExecAccessRead)...)
	withCoderScan(t, orgScan(true))
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, err := store.CreateConversationMode("default", "agent", "general", "claude", "", "opus", ai.ModeCoder, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetConversationOrgMode(conv.ID, "default", ai.OrgModeDynamic); err != nil {
		t.Fatal(err)
	}
	orig := orgStoredIDs
	orgStoredIDs = func(*turnJournal) ([]string, error) { return nil, errors.New("injected read error") }
	t.Cleanup(func() { orgStoredIDs = orig })

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "where is the cache?")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	j := parseJournaledTurn(t, out)
	found := false
	for _, e := range j.byType(chatevents.EventNotice) {
		var p chatevents.NoticePayload
		json.Unmarshal(e.Payload, &p)
		if p.Code == noticeOrgUnavailable && strings.Contains(p.Message, "injected read error") {
			found = true
		}
	}
	if !found {
		t.Error("the refusal to start the org must be explained in a notice that carries the error")
	}
	if n := len(j.byType(chatevents.EventAgentSpawned)); n != 0 {
		t.Errorf("%d workers were spawned although the stored ids could not be read", n)
	}
	logged, _ := os.ReadFile(argsLog)
	if strings.Contains(string(logged), "--tools-file") {
		t.Error("the lead must run without org tools when the org does not start")
	}
}
