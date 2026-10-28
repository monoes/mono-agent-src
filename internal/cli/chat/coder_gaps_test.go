package chat

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
)

func TestCoderTurnReportsFileExistedAndExitCodes(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "old.txt"), []byte("x"), 0o644)
	bin, _ := writeCoderMonomind(t, `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"tool_activity","id":"w1","phase":"start","name":"Write","input":{"file_path":"`+filepath.Join(cwd, "old.txt")+`","content":"y"}}'
  echo '{"v":1,"type":"tool_activity","id":"w1","phase":"end","name":"Write","ok":true,"output":"ok"}'
  echo '{"v":1,"type":"tool_activity","id":"w2","phase":"start","name":"Write","input":{"file_path":"new.txt","content":"y"}}'
  echo '{"v":1,"type":"tool_activity","id":"w2","phase":"end","name":"Write","ok":true,"output":"ok"}'
  echo '{"v":1,"type":"tool_activity","id":"b1","phase":"start","name":"Bash","input":{"command":"true"}}'
  echo '{"v":1,"type":"tool_activity","id":"b1","phase":"end","name":"Bash","ok":true,"output":""}'
  echo '{"v":1,"type":"tool_activity","id":"b2","phase":"start","name":"Bash","input":{"command":"false"}}'
  printf '%s\n' '{"v":1,"type":"tool_activity","id":"b2","phase":"end","name":"Bash","ok":false,"output":"Exit code 2\nboom"}'
  echo '{"v":1,"type":"tool_activity","id":"b3","phase":"start","name":"Bash","input":{"command":"sleep 9","run_in_background":true}}'
  echo '{"v":1,"type":"tool_activity","id":"b3","phase":"end","name":"Bash","ok":true,"output":"started"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
  echo '{"v":1,"type":"done","exit_code":0}'`)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, cwd)

	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	j := parseJournaledTurn(t, out)
	existed := map[string]*bool{}
	for _, e := range j.byType(chatevents.EventToolStarted) {
		var p chatevents.ToolStartedPayload
		json.Unmarshal(e.Payload, &p)
		existed[p.CallID] = p.FileExisted
	}
	if existed["w1"] == nil || !*existed["w1"] || existed["w2"] == nil || *existed["w2"] || existed["b1"] != nil {
		t.Errorf("fileExisted: w1=%v w2=%v b1=%v", existed["w1"], existed["w2"], existed["b1"])
	}
	codes := map[string]*int{}
	for _, e := range j.byType(chatevents.EventToolCompleted) {
		var p chatevents.ToolCompletedPayload
		json.Unmarshal(e.Payload, &p)
		codes[p.CallID] = p.ExitCode
	}
	if codes["b1"] == nil || *codes["b1"] != 0 || codes["b2"] == nil || *codes["b2"] != 2 || codes["b3"] != nil || codes["w1"] != nil {
		t.Errorf("exit codes: b1=%v b2=%v b3=%v w1=%v", codes["b1"], codes["b2"], codes["b3"], codes["w1"])
	}
}

func TestCoderStopBackgroundStopsOnlyTheSameProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("identity via /proc is Linux-only")
	}
	dbPath := newChatCLITestDB(t)
	marked := exec.Command("sleep", "60")
	marked.Env = append(os.Environ(), execTreeEnv+"=tok-123")
	stranger := exec.Command("sleep", "60")
	for _, c := range []*exec.Cmd{marked, stranger} {
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		// One goroutine reaps it (so a stopped process doesn't linger as a
		// zombie); cleanup kills and waits for that, never calling Wait
		// itself.
		reaped := make(chan struct{})
		go func(c *exec.Cmd) { c.Wait(); close(reaped) }(c)
		t.Cleanup(func() { c.Process.Kill(); <-reaped })
	}
	time.Sleep(100 * time.Millisecond)
	mp, sp := marked.Process.Pid, stranger.Process.Pid
	if got := processIdentity(mp); got != "tree:tok-123" {
		t.Fatalf("identity = %q", got)
	}

	bin, _ := writeCoderMonomind(t, `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"ok"}'
  echo '{"v":1,"type":"done","exit_code":0,"background_pids":[`+strconv.Itoa(mp)+`]}'`)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	store := openTestChatStore(t, dbPath)
	conv, _ := store.CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	if out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go"); err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	// A second turn's notice points at the stranger with an identity it
	// doesn't have: a reused pid.
	store.CreateTurn(conv.ID, "default", "turn-2", "i", "x")
	store.AppendEvent("default", conv.ID, "turn-2", chatevents.EventNotice, chatevents.NoticePayload{
		Code: noticeCoderBackground, Pids: []int{sp}, Processes: []chatevents.ProcessRef{{Pid: sp, Identity: "tree:tok-123"}},
	})

	out, code := runCoderCLI(t, dbPath, "stop-background", "--conversation", conv.ID, "--turn", "turn-2")
	var res backgroundStop
	decodeChatJSON(t, out, &res)
	if code != 0 || len(res.Refused) != 1 || res.Refused[0] != sp || !processAlive(sp) {
		t.Fatalf("reused pid: exit %d %+v alive=%v", code, res, processAlive(sp))
	}
	out, _ = runCoderCLI(t, dbPath, "stop-background", "--conversation", conv.ID, "--turn", "turn-1")
	decodeChatJSON(t, out, &res)
	if len(res.Stopped) != 1 || res.Stopped[0] != mp {
		t.Fatalf("stop: %+v", res)
	}
	for i := 0; i < 40 && processIdentity(mp) != ""; i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if processIdentity(mp) != "" {
		t.Error("the marked process is still running")
	}
	out, _ = runCoderCLI(t, dbPath, "stop-background", "--conversation", conv.ID, "--turn", "turn-1")
	decodeChatJSON(t, out, &res)
	if len(res.Gone) != 1 || len(res.Stopped) != 0 {
		t.Errorf("second stop: %+v", res)
	}
	if !strings.Contains(out, `"refused":[]`) {
		t.Errorf("arrays must be present, not null: %s", out)
	}
}

func TestCoderTurnClosesToolCallsLeftOpen(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"tool_activity","id":"b1","phase":"start","name":"Bash","input":{"command":"python3 -c pass"}}'
  echo '{"v":1,"type":"error","code":"cancelled","fatal":false,"message":"cancelled"}'
  echo '{"v":1,"type":"done","exit_code":130}'
  exit 130`)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	out, _ := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	j := parseJournaledTurn(t, out)
	done := j.byType(chatevents.EventToolCompleted)
	if len(done) != 1 {
		t.Fatalf("want the open call closed, got %d tool.completed", len(done))
	}
	var p chatevents.ToolCompletedPayload
	json.Unmarshal(done[0].Payload, &p)
	if p.CallID != "b1" || !p.Cancelled || p.OK == nil || *p.OK {
		t.Errorf("closing event = %+v", p)
	}
	fin := j.byType(chatevents.EventTurnFinished)
	if len(fin) != 1 || done[0].Seq > fin[0].Seq {
		t.Errorf("the call must close before turn.finished: %+v", j.events)
	}
}

// TestStreamedReplyStaysOnePart: chunks that arrive further apart than the
// coalescing window are still one text part until a tool call ends it, so
// the app renders one block instead of a line per chunk.
func TestStreamedReplyStaysOnePart(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	bin, _ := writeCoderMonomind(t, `  echo '{"v":1,"type":"start","runtime":"claude","cwd":"/w","pid":1,"access":"full"}'
  echo '{"v":1,"type":"assistant","text":"Created `+"`not"+`"}'
  sleep 0.3
  echo '{"v":1,"type":"assistant","text":"es.txt`+"`"+` with"}'
  sleep 0.3
  echo '{"v":1,"type":"tool_activity","id":"b1","phase":"start","name":"Bash","input":{"command":"ls"}}'
  echo '{"v":1,"type":"tool_activity","id":"b1","phase":"end","name":"Bash","ok":true,"output":""}'
  echo '{"v":1,"type":"assistant","text":"Done."}'
  echo '{"v":1,"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","text":"x"}'
  echo '{"v":1,"type":"done","exit_code":0}'`)
	withCoderCaps(t, monomind.CoderCapabilities...)
	setCoderSettings(t, dbPath, coderSettings{Enabled: true})
	conv, _ := openTestChatStore(t, dbPath).CreateConversationMode("default", "agent", "general", "claude", "", "", ai.ModeCoder, t.TempDir())
	out, err := runChatCmd(t, dbPath, bin, "--conversation", conv.ID, "--turn", "turn-1", "--", "go")
	if err != nil {
		t.Fatalf("turn: %v\n%s", err, out)
	}
	texts := map[string]string{}
	var order []string
	for _, e := range parseJournaledTurn(t, out).byType(chatevents.EventAssistantDelta) {
		var p chatevents.AssistantDeltaPayload
		json.Unmarshal(e.Payload, &p)
		if _, ok := texts[p.PartID]; !ok {
			order = append(order, p.PartID)
		}
		texts[p.PartID] += p.Text
	}
	if len(order) != 2 || texts[order[0]] != "Created `notes.txt` with" || texts[order[1]] != "Done." {
		t.Errorf("parts = %v %q", order, texts)
	}
}
