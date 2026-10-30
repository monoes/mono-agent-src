//go:build !windows

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

const convJSON = `{"id":"c1","profile_id":"work","backend":"agent","workflow_context":"general","runtime_id":"claude","model":"sonnet","session_id":"","mode":"coder","access":"coding","cwd":"/p","created_at":"a","updated_at":"b"}`

func TestListHistoryBuildsArgsAndDecodes(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"items":[{"id":"c1","kind":"chat","title":"Hello","status":"running","updatedAt":"u","taskId":"t1","applicationId":"app-1"},{"id":"org:a#r1","kind":"org","title":"a","status":"done","updatedAt":"u","orgName":"a","runId":"r1","costUsd":1.5}],"next_cursor":"u|c1","warnings":["org b: boom"]}'`+"\n")
	got, err := a.ListHistory("cur|x", 500, HistoryFilter{Kind: "chat", Status: "running", Application: "app-1", Query: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].Title != "Hello" || got.Items[0].TaskID != "t1" || got.Items[1].RunID != "r1" ||
		got.Items[1].CostUSD == nil || *got.Items[1].CostUSD != 1.5 || got.NextCursor != "u|c1" || len(got.Warnings) != 1 {
		t.Fatalf("page = %+v", got)
	}
	want := "--profile work --json chat history list --all --limit 200 --cursor cur|x --kind chat --status running --application app-1 --query hi"
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestListHistoryEmptyPageHasNoNils(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"items":null,"next_cursor":"","warnings":null}'`+"\n")
	got, err := a.ListHistory("", 0, HistoryFilter{})
	if err != nil || got.Items == nil || got.Warnings == nil {
		t.Fatalf("page = %+v, %v", got, err)
	}
}

func TestCreateChatConversationAccess(t *testing.T) {
	a, log := aitaskApp(t, "echo '"+convJSON+"'\n")
	got, err := a.CreateChatConversationAccess("general", "claude", "sonnet", "high", "coding", "")
	if err != nil || got.ID != "c1" || got.Access != "coding" || got.RuntimeID != "claude" || got.Cwd != "/p" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := a.CreateChatConversationAccess("general", "claude", "", "", "scoped", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateChatConversationAccess("general", "claude", "", "", "full", "/work"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--profile work --json chat history create --workflow general --runtime claude --model sonnet --effort high --access coding --profile-folder",
		"--profile work --json chat history create --workflow general --runtime claude --access scoped",
		"--profile work --json chat history create --workflow general --runtime claude --access full --cwd /work",
	}
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSetChatAccessRaiseGivesAScopedChatTheProfileFolder(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"scoped","cwd":""}';;
*) echo '{"id":"c1","access":"workspace","cwd":"/p","previous_access":"scoped"}';;
esac
`)
	got, err := a.SetChatAccess("c1", "workspace", "scoped", true)
	if err != nil || !got.OK || got.Conversation.Access != "workspace" || got.PreviousAccess != "scoped" || got.Lowered != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := "--profile work --json chat history show c1\n--profile work --json chat history set-access c1 workspace --expect scoped --profile-folder"
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != want {
		t.Fatalf("calls = %v", calls)
	}
}

// Each refusal happens before set-access is run.
func TestSetChatAccessGuards(t *testing.T) {
	for _, c := range []struct {
		name, level, expected, code, current string
		confirm                              bool
	}{
		{"stale expected", "scoped", "coding", "access_changed", "scoped", false},
		{"empty expected", "scoped", "", "expected_required", "", true},
		{"raise without confirm", "full", "scoped", "confirm_required", "scoped", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, log := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"scoped","cwd":""}';;
*) echo '{"id":"c1","access":"full","previous_access":"scoped"}';;
esac
`)
			got, err := a.SetChatAccess("c1", c.level, c.expected, c.confirm)
			if err != nil || got.OK || got.Code != c.code || got.Current != c.current {
				t.Fatalf("got %+v, %v", got, err)
			}
			if raw, err := os.ReadFile(log); err == nil && strings.Contains(string(raw), "set-access") {
				t.Fatalf("set-access ran: %s", raw)
			}
		})
	}
}

// A lowering needs no confirm.
func TestSetChatAccessLoweringNeedsNoConfirm(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"full","cwd":"/p"}';;
*) echo '{"id":"c1","access":"coding","cwd":"/p","previous_access":"full","lowered":{"stopped":[11],"survivors":[],"workers_dropped":true,"task_revoked":true}}';;
esac
`)
	got, err := a.SetChatAccess("c1", "coding", "full", false)
	l := got.Lowered
	if err != nil || !got.OK || l == nil || len(l.Stopped) != 1 || !l.WorkersDropped || !l.TaskRevoked {
		t.Fatalf("got %+v, %v", got, err)
	}
	if calls := loggedArgs(t, log); strings.HasSuffix(calls[1], "--profile-folder") {
		t.Fatalf("a lowering must not name a folder: %v", calls)
	}
}

// The level changed between our read and the CLI's: the CLI's result wins.
func TestSetChatAccessCLIResultWinsOverARace(t *testing.T) {
	a, _ := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"full","cwd":"/p"}';;
*) echo '{"id":"c1","access":"scoped","cwd":"/p","previous_access":"coding","lowered":{"stopped":[],"survivors":[],"workers_dropped":true,"task_revoked":false}}';;
esac
`)
	got, err := a.SetChatAccess("c1", "scoped", "full", false)
	if err != nil || !got.OK || got.PreviousAccess != "coding" || got.Current != "scoped" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The level changed between our read and the CLI's update: the CLI refuses
// (exit 6, access_changed) and the binding returns the typed result, with the
// expectation passed to the CLI.
func TestSetChatAccessMapsTheCLIsAccessChanged(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"full","cwd":"/p"}';;
*) echo '{"error":"the conversation'"'"'s access is coding, not the expected full","code":"access_changed","current":"coding"}'; exit 6;;
esac
`)
	got, err := a.SetChatAccess("c1", "scoped", "full", false)
	if err != nil || got.OK || got.Code != "access_changed" || got.Current != "coding" || got.Lowered != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
	if calls := loggedArgs(t, log); !strings.HasSuffix(calls[len(calls)-1], "set-access c1 scoped --expect full") {
		t.Fatalf("calls = %v", calls)
	}
}

func TestSetChatAccessRefusalIsAnError(t *testing.T) {
	a, _ := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"scoped","cwd":""}';;
*) echo '{"error":"only the user can raise","code":"operator_only"}'; exit 3;;
esac
`)
	if _, err := a.SetChatAccess("c1", "full", "scoped", true); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("err = %v", err)
	}
}

// A conversation whose turn is running is reported, not deleted, and the rest
// of the batch still goes.
func TestDeleteChatConversationsMapsTheActiveTurn(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*" delete busy"*) echo "chat: conversation has an active turn: stop the turn before deleting the conversation" >&2; exit 3;;
*" delete gone"*) echo "conversation not found" >&2; exit 3;;
*) echo '{"deleted":true}';;
esac
`)
	got, err := a.DeleteChatConversations([]string{"a", "busy", "gone", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Deleted, ",") != "a,b" || strings.Join(got.Active, ",") != "busy" || got.Failed["gone"] == "" || len(got.Failed) != 1 {
		t.Fatalf("got %+v", got)
	}
	if calls := loggedArgs(t, log); len(calls) != 4 || calls[1] != "--profile work --json chat history delete busy" {
		t.Fatalf("calls = %v", calls)
	}
}

// Exit 5 (survivors_remain) is a lowering that happened: the typed result comes
// back, not an error, and a trailing error document is ignored.
func TestSetChatAccessSurvivorsRemainIsNotAFailure(t *testing.T) {
	a, _ := aitaskApp(t, `case "$*" in
*" show "*) echo '{"id":"c1","access":"full","cwd":"/p"}';;
*) echo '{"id":"c1","access":"scoped","cwd":"/p","previous_access":"full","lowered":{"stopped":[],"survivors":[{"pid":12,"command":"sleep","reason":"not stopped"}],"workers_dropped":true,"task_revoked":false}}'
   echo '{"error":"the level is lowered, but 1 process(es) remain","code":"survivors_remain"}'; exit 5;;
esac
`)
	got, err := a.SetChatAccess("c1", "scoped", "full", false)
	if err != nil || !got.OK || got.Conversation.Access != "scoped" || got.Lowered == nil || len(got.Lowered.Survivors) != 1 || got.Lowered.Survivors[0].Pid != 12 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestGetChatConversationRecord(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"items":[{"id":"c1","kind":"chat","title":"Hello","access":"coding","mode":"coder","runtime":"claude","model":"sonnet","effort":"high","cwd":"/p","orgMode":"solo","workflowContext":"general"}],"next_cursor":"","warnings":[]}'`+"\n")
	got, err := a.GetChatConversationRecord("c1")
	if err != nil || !got.OK || got.Title != "Hello" || got.Access != "coding" || got.Runtime != "claude" || got.Cwd != "/p" || got.OrgMode != "solo" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != "--profile work --json chat history list --all --id c1 --limit 1" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestGetChatConversationRecordNotFound(t *testing.T) {
	a, _ := aitaskApp(t, `echo '{"items":[],"next_cursor":"","warnings":[]}'`+"\n")
	got, err := a.GetChatConversationRecord("nope")
	if err != nil || got.OK || got.Code != "not_found" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A delete the CLI declines is not a failure of the app: exit 5 (survivors_remain) keeps the chat and the
// survivors it printed come back by name, exit 3 operator_only comes back as a refusal to act on, and
// both are kept apart from the ids that failed or have an active turn.
func TestDeleteChatConversationsKeepsWhatTheCLIDeclined(t *testing.T) {
	a, log := aitaskApp(t, `case "$*" in
*" delete busy"*) echo '{"error":"chat: conversation has an active turn: stop the turn first","code":""}'; exit 3;;
*" delete running"*)
  echo '{"id":"running","deleted":false,"stopped":[7],"survivors":[{"pid":4242,"command":"node server.js","reason":"it outlived SIGKILL"},{"pid":4243,"command":"sleep 600","reason":"cannot be shown to be the turn'"'"'s"}]}'
  echo '{"error":"the conversation was not deleted: 2 process(es) its turns started could not be stopped","code":"survivors_remain"}'; exit 5;;
*" delete lead"*) echo '{"error":"delete a conversation that leads a mission: only the operator may","code":"operator_only"}'; exit 3;;
*" delete broken"*) echo "boom" >&2; exit 1;;
*) echo '{"deleted":true}';;
esac
`)
	got, err := a.DeleteChatConversations([]string{"a", "running", "busy", "lead", "broken", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Deleted, ",") != "a,b" || strings.Join(got.Active, ",") != "busy" {
		t.Fatalf("deleted/active = %+v", got)
	}
	run, ok := got.Kept["running"]
	if !ok || run.Code != "survivors_remain" || len(run.Survivors) != 2 || run.Survivors[0].Pid != 4242 || run.Survivors[0].Command != "node server.js" ||
		len(run.Stopped) != 1 || run.Stopped[0] != 7 || !strings.Contains(run.Message, "not deleted") {
		t.Fatalf("running = %+v (kept %+v)", run, got.Kept)
	}
	lead, ok := got.Kept["lead"]
	if !ok || lead.Code != "operator_only" || !strings.Contains(lead.Message, "operator") || lead.Survivors == nil || len(lead.Survivors) != 0 {
		t.Fatalf("lead = %+v", lead)
	}
	if len(got.Kept) != 2 || len(got.Failed) != 1 || got.Failed["broken"] == "" {
		t.Fatalf("kept %+v failed %+v", got.Kept, got.Failed)
	}
	if calls := loggedArgs(t, log); len(calls) != 6 {
		t.Fatalf("calls = %v", calls)
	}
}

// The single delete binding answers a declined delete with the code, the message and the survivors in its
// error document, not a bare string.
func TestDeleteChatConversationAnswersADeclinedDeleteWithItsSurvivors(t *testing.T) {
	a, _ := aitaskApp(t, `case "$*" in
*" delete running"*)
  echo '{"id":"running","deleted":false,"stopped":[],"survivors":[{"pid":4242,"command":"node server.js","reason":"it outlived SIGKILL"}]}'
  echo '{"error":"the conversation was not deleted: 1 process(es) could not be stopped","code":"survivors_remain"}'; exit 5;;
*" delete lead"*) echo '{"error":"only the operator may","code":"operator_only"}'; exit 3;;
*) echo '{"deleted":true}';;
esac
`)
	var doc struct {
		Error     string     `json:"error"`
		Code      string     `json:"code"`
		Survivors []Survivor `json:"survivors"`
	}
	if err := json.Unmarshal([]byte(a.DeleteChatConversation("running")), &doc); err != nil || doc.Code != "survivors_remain" ||
		len(doc.Survivors) != 1 || doc.Survivors[0].Pid != 4242 || !strings.Contains(doc.Error, "not deleted") {
		t.Fatalf("running: %+v %v", doc, err)
	}
	doc.Survivors = nil
	if err := json.Unmarshal([]byte(a.DeleteChatConversation("lead")), &doc); err != nil || doc.Code != "operator_only" {
		t.Fatalf("lead: %+v %v", doc, err)
	}
	if out := a.DeleteChatConversation("ok"); out != `{"ok":true}` {
		t.Fatalf("ok: %s", out)
	}
}

// "Delete anyway" is its own binding: it passes the CLI the words that leave the processes running, only for
// the ids it is given, and answers like the plain delete.
const anywayScript = `case "$*" in
*"--abandon-processes --confirm leave them running"*) echo '{"id":"x","deleted":true,"abandoned":[{"pid":4242}]}';;
*) echo '{"id":"x","deleted":false,"stopped":[],"survivors":[{"pid":4242,"command":"node server.js"}]}'; echo '{"error":"not deleted","code":"survivors_remain"}'; exit 5;;
esac
`

// asked records the native dialog's seam: what it was asked and what the person answers.
func asked(t *testing.T, answer bool) *[]string {
	t.Helper()
	var msgs []string
	old := confirmAbandon
	confirmAbandon = func(_ *App, _, msg string) bool { msgs = append(msgs, msg); return answer }
	t.Cleanup(func() { confirmAbandon = old })
	return &msgs
}

func abandoningCalls(t *testing.T, log string) int {
	t.Helper()
	if _, err := os.Stat(log); err != nil {
		return 0
	}
	n := 0
	for _, c := range loggedArgs(t, log) {
		if strings.Contains(c, "--abandon-processes") {
			n++
		}
	}
	return n
}

func TestDeleteAnywayRefusesAnIdTheCLINeverKept(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	msgs := asked(t, true)
	got, err := a.DeleteChatConversationsAnyway([]string{"never"})
	if err != nil || len(got.Deleted) != 0 || got.Failed["never"] == "" || len(*msgs) != 0 || abandoningCalls(t, log) != 0 {
		t.Fatalf("an id the CLI never kept was acted on: %+v %v asked %v", got, err, *msgs)
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Fatalf("the CLI was called: %v", loggedArgs(t, log))
	}
}

// The dialog shows the CLI's own list of processes, the delete carries the confirmation only after a yes, and
// the offer is used once.
func TestDeleteAnywayAsksNativelyWithTheCLIsListThenLeavesThemRunning(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	msgs := asked(t, true)
	if first, err := a.DeleteChatConversations([]string{"running"}); err != nil || first.Kept["running"].Code != "survivors_remain" {
		t.Fatalf("setup: %+v %v", first, err)
	}
	got, err := a.DeleteChatConversationsAnyway([]string{"running", "other"})
	if err != nil || strings.Join(got.Deleted, ",") != "running" || got.Failed["other"] == "" || len(got.Failed) != 1 {
		t.Fatalf("got %+v %v", got, err)
	}
	if len(*msgs) != 1 || !strings.Contains((*msgs)[0], "node server.js (pid 4242)") {
		t.Fatalf("dialog = %v, want the CLI's process list", *msgs)
	}
	if abandoningCalls(t, log) != 1 {
		t.Fatalf("calls = %v", loggedArgs(t, log))
	}
	again, _ := a.DeleteChatConversationsAnyway([]string{"running"})
	if len(again.Deleted) != 0 || abandoningCalls(t, log) != 1 || len(*msgs) != 1 {
		t.Fatalf("an offer was used twice: %+v", again)
	}
}

func TestDeleteAnywayDeclinedSendsNothing(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	msgs := asked(t, false)
	a.DeleteChatConversations([]string{"running"})
	got, err := a.DeleteChatConversationsAnyway([]string{"running"})
	if err != nil || len(got.Deleted) != 0 || len(*msgs) != 1 || abandoningCalls(t, log) != 0 {
		t.Fatalf("a declined dialog sent something: %+v %v calls %v", got, err, loggedArgs(t, log))
	}
	if got.Kept["running"].Code != "survivors_remain" {
		t.Fatalf("the chat is not reported kept: %+v", got)
	}
}

func TestDeleteAnywayRefusesAnExpiredOffer(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	asked(t, true)
	now := time.Now()
	old := abandonNow
	abandonNow = func() time.Time { return now }
	t.Cleanup(func() { abandonNow = old })
	a.DeleteChatConversations([]string{"running"})
	now = now.Add(offerLifetime + time.Second)
	got, _ := a.DeleteChatConversationsAnyway([]string{"running"})
	if len(got.Deleted) != 0 || got.Failed["running"] == "" || abandoningCalls(t, log) != 0 {
		t.Fatalf("an expired offer was acted on: %+v", got)
	}
}

// The plain delete never carries those words.
func TestDeleteChatConversationsNeverAbandonsProcesses(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"deleted":true}'`+"\n")
	if _, err := a.DeleteChatConversations([]string{"a"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range loggedArgs(t, log) {
		if strings.Contains(c, "abandon") {
			t.Fatalf("call = %s", c)
		}
	}
}

// An offer is the profile's: with another profile selected the id is refused and the CLI is not called.
func TestDeleteAnywayOfferBelongsToItsProfile(t *testing.T) {
	a, log := aitaskApp(t, anywayScript)
	asked(t, true)
	a.DeleteChatConversations([]string{"running"})
	other := "other-profile"
	a.selectedProfilePtr.Store(&other)
	got, err := a.DeleteChatConversationsAnyway([]string{"running"})
	if err != nil || len(got.Deleted) != 0 || got.Failed["running"] == "" || abandoningCalls(t, log) != 0 {
		t.Fatalf("an offer made under another profile was acted on: %+v %v", got, err)
	}
}

// The dialog lists a bounded number of bounded entries.
func TestDeleteAnywayDialogListIsBounded(t *testing.T) {
	var many []Survivor
	for i := 0; i < 25; i++ {
		many = append(many, Survivor{Pid: i + 1, Command: strings.Repeat("x", 500)})
	}
	lines := strings.Split(survivorLines(many), "\n")
	if len(lines) != dialogMaxSurvivors+1 || lines[dialogMaxSurvivors] != "… and 15 more" {
		t.Fatalf("lines = %d, last %q", len(lines), lines[len(lines)-1])
	}
	for _, l := range lines[:dialogMaxSurvivors] {
		if len([]rune(l)) > dialogMaxEntry {
			t.Fatalf("entry is %d characters", len([]rune(l)))
		}
	}
	if got := survivorLines([]Survivor{{Pid: 7, Command: "sleep"}}); got != "sleep (pid 7)" {
		t.Fatalf("short list = %q", got)
	}
}
