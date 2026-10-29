//go:build !windows

package main

import (
	"os"
	"strings"
	"testing"
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

func TestRenameChatConversationShellsToTheCLI(t *testing.T) {
	a, log := aitaskApp(t, `echo '{"id":"c1","title":"meta challenge"}'
`)
	got, err := a.RenameChatConversation("c1", "meta challenge")
	if err != nil || got != "meta challenge" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := a.RenameChatConversation("c1", "-dash"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RenameChatConversation("c1", "  "); err != nil {
		t.Fatal(err)
	}
	want := "--profile work --json chat history rename c1 -- meta challenge\n" +
		"--profile work --json chat history rename c1 -- -dash\n" +
		"--profile work --json chat history rename c1"
	if calls := loggedArgs(t, log); strings.Join(calls, "\n") != want {
		t.Fatalf("calls = %v", calls)
	}
}
