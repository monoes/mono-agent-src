//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
)

// fakeChatReply is one canned answer of the fake monoagentcli: the first
// reply whose match is a substring of the argv answers.
type fakeChatReply struct {
	match  string
	stdout string
	stderr string
	code   int
	hang   bool // keep running after the output, until killed
}

// chatFakeCLI writes a fake monoagentcli that logs each argv (one line per
// call) and answers with the given replies.
func chatFakeCLI(t *testing.T, replies ...fakeChatReply) (bin, argsLog string) {
	t.Helper()
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	var body strings.Builder
	fmt.Fprintf(&body, "printf '%%s\\n' \"$*\" >> '%s'\ncase \"$*\" in\n", argsLog)
	for i, r := range replies {
		out := filepath.Join(dir, fmt.Sprintf("out%d", i))
		errf := filepath.Join(dir, fmt.Sprintf("err%d", i))
		os.WriteFile(out, []byte(r.stdout), 0o600)
		os.WriteFile(errf, []byte(r.stderr), 0o600)
		hang := ""
		if r.hang {
			hang = "sleep 30; "
		}
		fmt.Fprintf(&body, "  *%q*) cat '%s'; cat '%s' >&2; %sexit %d;;\n", r.match, out, errf, hang, r.code)
	}
	body.WriteString("esac\necho \"fake monoagentcli: unexpected call: $*\" >&2\nexit 99\n")
	return fakeCLI(t, body.String()), argsLog
}

func readArgsLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// withoutCoderProbe drops the `chat history show` StartChatTurn makes first to
// learn whether the conversation is a coder one (the host's) or a scoped chat;
// the tests that use it are about what follows.
func withoutCoderProbe(calls []string) []string {
	var out []string
	for _, l := range calls {
		if !strings.Contains(l, "chat history show") {
			out = append(out, l)
		}
	}
	return out
}

// newCLIChatApp builds an App whose chat supervisor runs real processes
// against bin.
func newCLIChatApp(t *testing.T, bin string) (*App, *collectingEmitter) {
	t.Helper()
	emitter := &collectingEmitter{}
	sup := newChatSupervisor(defaultChatProcessLauncher, emitter.emit, func() (string, error) { return bin, nil })
	t.Cleanup(sup.stopAll)
	a := &App{chatSup: sup}
	a.selectForTest("default")
	return a, emitter
}

func lines(ls ...string) string { return strings.Join(ls, "\n") + "\n" }

func finishReply(conversationID, turnID string, status chatevents.TurnStatus, seq int64) fakeChatReply {
	ev, _ := chatevents.New("default", conversationID, turnID, seq, time.Now(), chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: status, HistorySaved: true})
	rec := ev.Record()
	b, _ := json.Marshal(map[string]any{"finalized": true, "event": rec, "turn": ai.TurnRecord{ID: turnID, Status: string(status)}})
	return fakeChatReply{match: "chat history finish", stdout: string(b)}
}

type startChatTurnResponse struct {
	OK     bool   `json:"ok"`
	TurnID string `json:"turnId"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

func decodeStart(t *testing.T, s string) startChatTurnResponse {
	t.Helper()
	var r startChatTurnResponse
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("unmarshal %s: %v", s, err)
	}
	return r
}

// --- StartChatTurn and the turn process ---

func TestApp_StartChatTurn_RunsTheCLITurnAndRelaysItsEvents(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat --conversation", stdout: lines(
		admissionLine("conv-1", "turn-1", "active", false),
		eventLine("conv-1", "turn-1", 1, chatevents.EventTurnStarted, chatevents.TurnStartedPayload{Backend: "agent", Text: "hello world"}),
		eventLine("conv-1", "turn-1", 2, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "part-1", Text: "hi!"}),
		eventLine("conv-1", "turn-1", 3, chatevents.EventTurnFinished, chatevents.TurnFinishedPayload{Status: chatevents.StatusCompleted, HistorySaved: true}),
	)})
	a, emitter := newCLIChatApp(t, bin)
	a.selectForTest("work")

	r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hello world", true, true, nil))
	if !r.OK || r.TurnID != "turn-1" || r.Status != "active" {
		t.Fatalf("Start = %+v", r)
	}
	waitForType(t, emitter, chatevents.EventTurnFinished, 5*time.Second)
	waitReleased(t, a.chatSup, "conv-1", "turn-1")
	if n := len(emitter.snapshot()); n != 3 {
		t.Errorf("emitted %d events, want 3", n)
	}
	got := withoutCoderProbe(readArgsLog(t, argsLog))
	want := "--profile work --json chat --conversation conv-1 --turn turn-1 --instance " + a.chatSup.instanceID + " --tools monoagent,runs -- hello world"
	if len(got) != 1 || got[0] != want {
		t.Errorf("CLI calls = %q\nwant one: %q (a turn that finished itself needs no finish call)", got, want)
	}
}

func TestApp_StartChatTurn_BusyAndDuplicateResponsesIncludeTurnID_ThenStop(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "chat --conversation", hang: true, stdout: lines(
			admissionLine("conv-1", "turn-1", "active", false),
			eventLine("conv-1", "turn-1", 1, chatevents.EventTurnStarted, chatevents.TurnStartedPayload{Backend: "agent", Text: "hi"}),
		)},
		finishReply("conv-1", "turn-1", chatevents.StatusCancelled, 2),
	)
	a, emitter := newCLIChatApp(t, bin)

	if r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil)); !r.OK || r.Status != "active" {
		t.Fatalf("first Start = %+v", r)
	}
	r2 := decodeStart(t, a.StartChatTurn("conv-1", "turn-2", "hi again", false, false, nil))
	if r2.OK || r2.Status != "busy" || r2.TurnID != "turn-2" {
		t.Errorf("Start of another turn while turn-1 runs = %+v, want ok=false status=busy turnId=turn-2", r2)
	}
	r3 := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil))
	if !r3.OK || r3.Status != "active" || r3.TurnID != "turn-1" {
		t.Errorf("duplicate Start = %+v, want ok=true status=active turnId=turn-1", r3)
	}

	if resp := a.StopChatTurn("conv-1", "turn-1"); resp != `{"ok":true}` {
		t.Fatalf("Stop = %s", resp)
	}
	finished := waitForType(t, emitter, chatevents.EventTurnFinished, 5*time.Second)
	var p chatevents.TurnFinishedPayload
	jsonUnmarshalPayload(finished[0], &p)
	if p.Status != chatevents.StatusCancelled || finished[0].Seq != 2 {
		t.Errorf("turn.finished after Stop = %+v seq %d", p, finished[0].Seq)
	}
	waitReleased(t, a.chatSup, "conv-1", "turn-1")
	calls := withoutCoderProbe(readArgsLog(t, argsLog))
	if len(calls) != 2 || calls[1] != "--profile default --json chat history finish conv-1 turn-1 --status cancelled --reason stopped" {
		t.Errorf("CLI calls = %q, want the turn then its cancelled finish", calls)
	}
}

func TestApp_StartChatTurn_RetriedFinishedTurnReportsItsStatus(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "chat --conversation", stdout: lines(admissionLine("conv-1", "turn-1", "completed", true))})
	a, emitter := newCLIChatApp(t, bin)
	r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil))
	if !r.OK || r.Status != "completed" || r.TurnID != "turn-1" {
		t.Errorf("retried Start = %+v, want ok=true status=completed", r)
	}
	if a.chatSup.lookup("conv-1", "turn-1") != nil {
		t.Error("a turn that already ran is still admitted")
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(emitter.snapshot()); n != 0 {
		t.Errorf("emitted %d events for a turn that did not run", n)
	}
}

func TestApp_StartChatTurn_CLIRefusalsAreErrorsAndReleaseTheSlot(t *testing.T) {
	cases := []struct {
		name, stderr, want string
		code               int
	}{
		{"provider conversation", "warning: something harmless\nconversation conv-1 used a removed AI provider and is read-only; start a new chat\n", "read-only", 3},
		{"another instance owns the active turn", "chat: conversation has an active turn owned by another instance\n", "another instance", 3},
		{"unknown conversation", "chat: conversation not found\n", "not found", 2},
		{"CLI older than the app", "Error: unknown flag: --conversation\n", "update monoagentcli", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin, _ := chatFakeCLI(t, fakeChatReply{match: "chat --conversation", stderr: tc.stderr, code: tc.code})
			a, emitter := newCLIChatApp(t, bin)
			r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil))
			if r.OK || !strings.Contains(r.Error, tc.want) {
				t.Errorf("Start = %+v, want an error containing %q", r, tc.want)
			}
			if strings.Contains(r.Error, "harmless") {
				t.Errorf("error carries stderr warnings, not the CLI's error line: %q", r.Error)
			}
			if a.chatSup.lookup("conv-1", "turn-1") != nil {
				t.Error("refused turn still admitted — the conversation would wedge as busy")
			}
			if n := len(emitter.snapshot()); n != 0 {
				t.Errorf("emitted %d events for a refused turn", n)
			}
		})
	}
}

// A refusal the CLI classified keeps its code: the chat panel links an
// agent_not_setup refusal to the AI agents page.
func TestApp_StartChatTurn_RefusalKeepsTheCLIErrorCode(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{
		match:  "chat --conversation",
		stdout: `{"code":"agent_not_setup","error":"monomind not found (AI engine)"}` + "\n",
		stderr: "monomind not found (AI engine)\n",
		code:   1,
	})
	a, _ := newCLIChatApp(t, bin)
	var r struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	if err := json.Unmarshal([]byte(a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil)), &r); err != nil {
		t.Fatal(err)
	}
	if r.Code != "agent_not_setup" || !strings.Contains(r.Error, "monomind not found") {
		t.Errorf("Start = %+v, want the agent_not_setup code", r)
	}
	if a.chatSup.lookup("conv-1", "turn-1") != nil {
		t.Error("refused turn still admitted")
	}
}

func TestApp_TurnProcessDyingUnfinished_IsFinishedThroughTheCLI(t *testing.T) {
	cases := []struct {
		name       string
		code       int
		wantStatus string
		wantArgs   string
	}{
		{"crash", 7, "failed", "--status failed --reason monoagentcli exited: exit status 7: segmentation fault --exit-code 7"},
		{"clean exit without turn.finished", 0, "interrupted", "--status interrupted --reason monoagentcli ended without finishing the turn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin, argsLog := chatFakeCLI(t,
				fakeChatReply{match: "chat --conversation", code: tc.code, stderr: "segmentation fault\n", stdout: lines(
					admissionLine("conv-1", "turn-1", "active", false),
					eventLine("conv-1", "turn-1", 1, chatevents.EventTurnStarted, chatevents.TurnStartedPayload{Backend: "agent", Text: "hi"}),
				)},
				finishReply("conv-1", "turn-1", chatevents.TurnStatus(tc.wantStatus), 2),
			)
			a, emitter := newCLIChatApp(t, bin)
			if r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil)); !r.OK {
				t.Fatalf("Start = %+v", r)
			}
			finished := waitForType(t, emitter, chatevents.EventTurnFinished, 5*time.Second)
			var p chatevents.TurnFinishedPayload
			jsonUnmarshalPayload(finished[0], &p)
			if string(p.Status) != tc.wantStatus {
				t.Errorf("status = %s, want %s", p.Status, tc.wantStatus)
			}
			waitReleased(t, a.chatSup, "conv-1", "turn-1")
			calls := withoutCoderProbe(readArgsLog(t, argsLog))
			want := "--profile default --json chat history finish conv-1 turn-1 " + tc.wantArgs
			if len(calls) != 2 || calls[1] != want {
				t.Errorf("finish call = %q\nwant %q", calls, want)
			}
		})
	}
}

// If even the finish call fails, the UI still gets a live-only turn.finished
// instead of hanging: historySaved false, and seq MaxSafeSeq so the
// frontend's "ev.seq <= state.lastSeq" dedup never discards it.
func TestApp_FinishFailure_EmitsLiveOnlyTurnFinished(t *testing.T) {
	bin, _ := chatFakeCLI(t,
		fakeChatReply{match: "chat --conversation", code: 1, stdout: lines(admissionLine("conv-1", "turn-1", "active", false))},
		fakeChatReply{match: "chat history finish", code: 1, stderr: "database is locked\n"},
	)
	a, emitter := newCLIChatApp(t, bin)
	if r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil)); !r.OK {
		t.Fatalf("Start = %+v", r)
	}
	finished := waitForType(t, emitter, chatevents.EventTurnFinished, 5*time.Second)
	var p chatevents.TurnFinishedPayload
	jsonUnmarshalPayload(finished[0], &p)
	if p.Status != chatevents.StatusFailed || p.HistorySaved || finished[0].Seq != chatevents.MaxSafeSeq {
		t.Errorf("live-only turn.finished = %+v seq %d", p, finished[0].Seq)
	}
	waitReleased(t, a.chatSup, "conv-1", "turn-1")
}

func TestApp_StopAll_KillsRunningTurnsAndWaitsForTheirFinish(t *testing.T) {
	bin, _ := chatFakeCLI(t,
		fakeChatReply{match: "chat --conversation", hang: true, stdout: lines(admissionLine("conv-1", "turn-1", "active", false))},
		finishReply("conv-1", "turn-1", chatevents.StatusCancelled, 1),
	)
	a, emitter := newCLIChatApp(t, bin)
	if r := decodeStart(t, a.StartChatTurn("conv-1", "turn-1", "hi", false, false, nil)); !r.OK {
		t.Fatalf("Start = %+v", r)
	}
	a.chatSup.stopAll()
	if got := emitter.byType(chatevents.EventTurnFinished); len(got) != 1 {
		t.Fatalf("stopAll returned before the cancelled finish was recorded: %+v", emitter.snapshot())
	}
	if a.chatSup.lookup("conv-1", "turn-1") != nil {
		t.Error("turn still admitted after stopAll")
	}
}

// --- history bindings ---

func TestApp_CreateChatConversation_ShellsOutAndKeepsTheShape(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history create", stdout: `{"id":"c1","profile_id":"default","backend":"agent","workflow_context":"general","runtime_id":"fake-runtime","provider_id":"","model":"m1","effort":"high","session_id":"","mode":"assistant","access":"","cwd":"","created_at":"2026-09-26T10:00:00Z","updated_at":"2026-09-26T10:00:00Z"}`})
	a, _ := newCLIChatApp(t, bin)
	out := a.CreateChatConversation("general", "fake-runtime", "m1", "high")
	want := `{"id":"c1","profileId":"default","backend":"agent","workflowContext":"general","runtimeId":"fake-runtime","model":"m1","effort":"high","mode":"assistant","access":"","cwd":"","createdAt":"2026-09-26T10:00:00Z","updatedAt":"2026-09-26T10:00:00Z"}`
	if out != want {
		t.Errorf("CreateChatConversation = %s\nwant %s", out, want)
	}
	a.CreateChatConversation("draft", "codex", "", "")
	got := readArgsLog(t, argsLog)
	wantArgs := []string{
		"--profile default --json chat history create --runtime fake-runtime --workflow general --model m1 --effort high",
		"--profile default --json chat history create --runtime codex --workflow draft",
	}
	if strings.Join(got, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("argv = %q\nwant %q", got, wantArgs)
	}
}

func TestApp_ListChatConversations_ShellsOut(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history list", stdout: `{"items":[{"id":"c1","profile_id":"work","backend":"provider","workflow_context":"general","runtime_id":"","provider_id":"old","model":"gpt-x","session_id":"","mode":"assistant","access":"","cwd":"","created_at":"t0","updated_at":"t1"},{"id":"c2","profile_id":"work","backend":"agent","workflow_context":"general","runtime_id":"claude","provider_id":"","model":"sonnet","session_id":"","mode":"coder","access":"","cwd":"/w/proj","created_at":"t0","updated_at":"t0"}],"next_cursor":"t1|c1"}`})
	a, _ := newCLIChatApp(t, bin)
	a.selectForTest("work")
	out := a.ListChatConversations("t9|c9", 999)
	want := `{"items":[{"id":"c1","profileId":"work","backend":"provider","workflowContext":"general","providerId":"old","model":"gpt-x","mode":"assistant","access":"","cwd":"","createdAt":"t0","updatedAt":"t1"},{"id":"c2","profileId":"work","backend":"agent","workflowContext":"general","runtimeId":"claude","model":"sonnet","mode":"coder","access":"","cwd":"/w/proj","createdAt":"t0","updatedAt":"t0"}],"nextCursor":"t1|c1"}`
	if out != want {
		t.Errorf("ListChatConversations = %s\nwant %s", out, want)
	}
	a.ListChatConversations("", 0)
	got := readArgsLog(t, argsLog)
	wantArgs := []string{
		"--profile work --json chat history list --limit 200 --cursor t9|c9",
		"--profile work --json chat history list --limit 50",
	}
	if strings.Join(got, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("argv = %q\nwant %q", got, wantArgs)
	}
}

func TestApp_ListChatConversations_EmptyIsAnArray(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "chat history list", stdout: `{"items":[],"next_cursor":""}`})
	a, _ := newCLIChatApp(t, bin)
	if out := a.ListChatConversations("", 10); out != `{"items":[],"nextCursor":""}` {
		t.Errorf("empty list = %s", out)
	}
}

// Two live instances sharing one database: each sees the other's active
// turn read-only (ownedByThisInstance false), finished turns never are,
// and no raw instance id reaches the frontend.
func TestApp_GetChatTurns_OwnedByThisInstance(t *testing.T) {
	a, _ := newCLIChatApp(t, "")
	b, _ := newCLIChatApp(t, "")
	page := func(owner string) string {
		out, _ := json.Marshal(map[string]any{"items": []ai.TurnRecord{
			{ID: "turn-active-a", ConversationID: "c1", ProfileID: "default", OwnerInstanceID: owner, Status: "active", LastCommittedSeq: 4},
			{ID: "turn-old", ConversationID: "c1", ProfileID: "default", OwnerInstanceID: b.chatSup.instanceID, Status: "completed"},
		}, "next_cursor": "x|turn-old"})
		return string(out)
	}
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history turns", stdout: page(a.chatSup.instanceID)})
	a.chatSup.findCLI = func() (string, error) { return bin, nil }
	b.chatSup.findCLI = a.chatSup.findCLI

	type turnItem struct {
		ID                  string `json:"id"`
		Status              string `json:"status"`
		LastCommittedSeq    int64  `json:"lastCommittedSeq"`
		OwnedByThisInstance bool   `json:"ownedByThisInstance"`
	}
	decode := func(s string) (items map[string]turnItem, next string) {
		var r struct {
			Items      []turnItem `json:"items"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal([]byte(s), &r); err != nil {
			t.Fatalf("unmarshal %s: %v", s, err)
		}
		items = map[string]turnItem{}
		for _, it := range r.Items {
			items[it.ID] = it
		}
		return items, r.NextCursor
	}
	outA := a.GetChatTurns("c1", "", 20)
	itemsA, next := decode(outA)
	if !itemsA["turn-active-a"].OwnedByThisInstance || !itemsA["turn-old"].OwnedByThisInstance || next != "x|turn-old" || itemsA["turn-active-a"].LastCommittedSeq != 4 {
		t.Errorf("A's view = %s", outA)
	}
	outB := b.GetChatTurns("c1", "x|y", 0)
	itemsB, _ := decode(outB)
	if itemsB["turn-active-a"].OwnedByThisInstance || !itemsB["turn-old"].OwnedByThisInstance {
		t.Errorf("B's view = %s, want A's active turn read-only and finished history owned", outB)
	}
	for _, out := range []string{outA, outB} {
		if strings.Contains(out, a.chatSup.instanceID) || strings.Contains(out, b.chatSup.instanceID) || strings.Contains(out, "ownerInstanceId") {
			t.Errorf("GetChatTurns leaks an instance id: %s", out)
		}
	}
	got := readArgsLog(t, argsLog)
	wantArgs := []string{
		"--profile default --json chat history turns c1 --limit 20",
		"--profile default --json chat history turns c1 --limit 50 --cursor x|y",
	}
	if strings.Join(got, "|") != strings.Join(wantArgs, "|") {
		t.Errorf("argv = %q\nwant %q", got, wantArgs)
	}
}

func TestApp_GetChatTurns_UnknownConversationIsEmpty(t *testing.T) {
	bin, _ := chatFakeCLI(t, fakeChatReply{match: "chat history turns", code: 2, stderr: "chat: conversation not found\n"})
	a, _ := newCLIChatApp(t, bin)
	if out := a.GetChatTurns("nope", "", 10); out != `{"items":[],"nextCursor":"","notFound":true}` {
		t.Errorf("unknown conversation = %s", out)
	}
}

func TestApp_GetChatEvents_ShellsOutAndKeepsTheShape(t *testing.T) {
	rec := eventLine("c1", "t1", 4, chatevents.EventAssistantDelta, chatevents.AssistantDeltaPayload{PartID: "part-1", Text: "hi"})
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "events c1 t1", stdout: `{"items":[` + rec + `],"turn":{"id":"t1","status":"active"},"last_committed_seq":9,"has_more":true}`},
		fakeChatReply{match: "events c1 missing", code: 2, stderr: "chat: turn not found\n"},
		fakeChatReply{match: "events c1 broken", code: 1, stderr: "database is locked\n"},
	)
	a, _ := newCLIChatApp(t, bin)
	var got struct {
		Items            []map[string]any `json:"items"`
		LastCommittedSeq int64            `json:"lastCommittedSeq"`
		HasMore          bool             `json:"hasMore"`
	}
	out := a.GetChatEvents("c1", "t1", 3, 999)
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0]["turnId"] != "t1" || got.Items[0]["seq"] != float64(4) || got.LastCommittedSeq != 9 || !got.HasMore {
		t.Errorf("GetChatEvents = %s", out)
	}
	if payload, _ := got.Items[0]["payload"].(map[string]any); payload["partId"] != "part-1" {
		t.Errorf("payload not passed through: %s", out)
	}
	if out := a.GetChatEvents("c1", "missing", 0, 10); out != `{"items":[],"lastCommittedSeq":0,"hasMore":false}` {
		t.Errorf("unknown turn = %s", out)
	}
	if out := a.GetChatEvents("c1", "broken", 0, 10); !strings.Contains(out, `"error":"database is locked"`) {
		t.Errorf("CLI failure = %s", out)
	}
	calls := readArgsLog(t, argsLog)
	if calls[0] != "--profile default --json chat history events c1 t1 --after-seq 3 --limit 200" {
		t.Errorf("argv = %q", calls[0])
	}
}

func TestApp_DeleteChatConversation_ShellsOut(t *testing.T) {
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "delete busy", code: 3, stderr: "chat: conversation has an active turn: stop the turn before deleting the conversation\n"},
		fakeChatReply{match: "delete c1", stdout: `{"id":"c1","deleted":true}`},
	)
	a, _ := newCLIChatApp(t, bin)
	if out := a.DeleteChatConversation("c1"); out != `{"ok":true}` {
		t.Errorf("delete = %s", out)
	}
	if out := a.DeleteChatConversation("busy"); !strings.Contains(out, `"error":"chat: conversation has an active turn`) {
		t.Errorf("delete with an active turn = %s", out)
	}
	if calls := readArgsLog(t, argsLog); calls[0] != "--profile default --json chat history delete c1" {
		t.Errorf("argv = %q", calls)
	}
}

func TestApp_StopChatTurn_ForeignUnknownAndFinishedTurns(t *testing.T) {
	a, _ := newCLIChatApp(t, "")
	foreign, _ := json.Marshal(ai.TurnRecord{ID: "t-foreign", ConversationID: "c1", Status: "active", OwnerInstanceID: "someone-else"})
	finished, _ := json.Marshal(ai.TurnRecord{ID: "t-done", ConversationID: "c1", Status: "completed", OwnerInstanceID: "someone-else"})
	legacy, _ := json.Marshal(ai.TurnRecord{ID: "t-legacy", ConversationID: "c1", Status: "active"})
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "turn c1 t-foreign", stdout: string(foreign)},
		fakeChatReply{match: "turn c1 t-done", stdout: string(finished)},
		fakeChatReply{match: "turn c1 t-legacy", stdout: string(legacy)},
		fakeChatReply{match: "turn c1 t-unknown", code: 2, stderr: "chat: turn not found\n"},
	)
	a.chatSup.findCLI = func() (string, error) { return bin, nil }

	var r struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.Unmarshal([]byte(a.StopChatTurn("c1", "t-foreign")), &r)
	if r.OK || r.Error == "" {
		t.Errorf("Stop of another window's active turn = %+v, want an explicit failure", r)
	}
	for _, id := range []string{"t-done", "t-legacy", "t-unknown"} {
		if out := a.StopChatTurn("c1", id); out != `{"ok":true}` {
			t.Errorf("Stop %s = %s, want the idempotent no-op", id, out)
		}
	}
	if calls := readArgsLog(t, argsLog); calls[0] != "--profile default --json chat history turn c1 t-foreign" {
		t.Errorf("argv = %q", calls)
	}
}

func TestApp_StopChatAgent_ShellsOutToChatTurnStop(t *testing.T) {
	a, _ := newCLIChatApp(t, "")
	bin, argsLog := chatFakeCLI(t,
		fakeChatReply{match: "chat turn stop --agent=w2 --wait 20s -- c1 t1", stdout: `{"agent_id":"w2","status":"cancelled","requested":true,"turn_status":"active"}`},
		fakeChatReply{match: "-- c1 t-unknown", code: 2, stderr: "chat: turn not found\n"},
	)
	a.chatSup.findCLI = func() (string, error) { return bin, nil }

	var r struct {
		OK        bool   `json:"ok"`
		Status    string `json:"status"`
		Requested bool   `json:"requested"`
		Error     string `json:"error"`
	}
	json.Unmarshal([]byte(a.StopChatAgent("c1", "t1", "w2")), &r)
	if !r.OK || r.Status != "cancelled" || !r.Requested {
		t.Errorf("StopChatAgent = %+v", r)
	}
	r = struct {
		OK        bool   `json:"ok"`
		Status    string `json:"status"`
		Requested bool   `json:"requested"`
		Error     string `json:"error"`
	}{}
	json.Unmarshal([]byte(a.StopChatAgent("c1", "t-unknown", "w2")), &r)
	if r.OK || !strings.Contains(r.Error, "turn not found") {
		t.Errorf("StopChatAgent on an unknown turn = %+v, want the CLI's error", r)
	}
	if calls := readArgsLog(t, argsLog); calls[0] != "--profile default --json chat turn stop --agent=w2 --wait 20s -- c1 t1" {
		t.Errorf("argv = %q", calls)
	}
}

func TestChatSupervisor_ReconcileShellsOutExceptItsOwnTurns(t *testing.T) {
	bin, argsLog := chatFakeCLI(t, fakeChatReply{match: "chat history reconcile", code: 1,
		stdout: `{"reconciled":[{"id":"t1","status":"interrupted"}],"errors":["reconcile turn t2: database is locked"]}`,
		stderr: "reconcile: 1 turn(s) failed: reconcile turn t2: database is locked\n"})
	a, _ := newCLIChatApp(t, bin)
	errs := a.chatSup.reconcileOrphanedTurns()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "t2") {
		t.Errorf("errs = %v", errs)
	}
	want := "--json chat history reconcile --except-owner " + a.chatSup.instanceID
	if calls := readArgsLog(t, argsLog); len(calls) != 1 || calls[0] != want {
		t.Errorf("argv = %q, want %q (unscoped by profile)", calls, want)
	}

	bin2, _ := chatFakeCLI(t)
	a.chatSup.findCLI = func() (string, error) { return bin2, nil }
	if errs := a.chatSup.reconcileOrphanedTurns(); len(errs) != 1 {
		t.Errorf("a failing CLI must surface as an error: %v", errs)
	}
}

func TestAIErrorCarriesAgentNotSetupCode(t *testing.T) {
	var body map[string]string
	json.Unmarshal([]byte(aiError(errors.New("agent.ask: monomind not found "+agentNotSetupMarker))), &body)
	if body["code"] != "agent_not_setup" || !strings.Contains(body["error"], "monomind not found") {
		t.Errorf("aiError(marked) = %v", body)
	}
	json.Unmarshal([]byte(aiError(&codedError{msg: "not set up", code: "agent_not_setup"})), &body)
	if body["code"] != "agent_not_setup" {
		t.Errorf("aiError(coded) = %v", body)
	}
	if got := aiError(errors.New("boom")); got != `{"error":"boom"}` {
		t.Errorf("aiError(boom) = %s", got)
	}
}

func TestAgentSetupWatchMarksFailedRun(t *testing.T) {
	w := &agentSetupWatch{}
	w.note("Status: FAILED")
	ev := w.apply(map[string]interface{}{"workflow_id": "w1", "success": false})
	if _, ok := ev["code"]; ok {
		t.Errorf("unmarked output gave %v", ev)
	}
	w.note("Error:  node n1 (Ask): agent.ask (claude) turn failed: runner-error: Not logged in " + agentNotSetupMarker)
	ev = w.apply(map[string]interface{}{"workflow_id": "w1", "success": false})
	if ev["code"] != "agent_not_setup" || !strings.Contains(ev["error"].(string), "Not logged in") {
		t.Errorf("marked output gave %v", ev)
	}
}
