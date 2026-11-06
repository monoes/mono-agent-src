package chat

import (
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/storage"
)

// startTurn gives the fixture's conversation its first message (a turn row), as the send path does.
func (f toggleFixture) startTurn(t *testing.T) {
	t.Helper()
	db, err := storage.NewDatabase(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.DB.Exec(`INSERT INTO ai_chat_turns (id, conversation_id, profile_id, prompt, created_at, updated_at) VALUES ('t1', ?, 'default', 'hi', 'x', 'x')`, f.conv.ID); err != nil {
		t.Fatal(err)
	}
}

func TestChatModeIsSetBeforeTheFirstMessageAndLockedAfter(t *testing.T) {
	f := newToggleFixture(t)
	v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if v.ChatMode.Mode != "off" || v.ChatMode.Locked || len(v.ChatMode.Options) != 3 {
		t.Fatalf("a new chat: %+v", v.ChatMode)
	}
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "mission"); code != 0 || v.ChatMode.Mode != "mission" || !v.Mission.On || !v.Settings.Mission {
		t.Fatalf("set mission: exit %d %+v", code, v.ChatMode)
	}
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "off"); code != 0 || v.ChatMode.Mode != "off" || v.Mission.On {
		t.Fatalf("back to off before any message: exit %d %+v", code, v.ChatMode)
	}
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "mission"); code != 0 {
		t.Fatal("mission again")
	}
	f.startTurn(t)
	v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID)
	if !v.ChatMode.Locked || v.ChatMode.Reason == "" || v.ChatMode.Mode != "mission" {
		t.Fatalf("after the first message: %+v", v.ChatMode)
	}
	for _, args := range [][]string{
		{"--chat-mode", "off"}, {"--mission", "off"}, {"--patch", `{"chat_mode":"off"}`}, {"--patch", `{"mission":false}`},
	} {
		out, _, code := f.run(t, f.cfg, append([]string{"settings", "set", f.conv.ID}, args...)...)
		if code == 0 {
			t.Errorf("%v was accepted after the first message", args)
		}
		_ = out
	}
	if v, _, _ = f.run(t, f.cfg, "settings", "get", f.conv.ID); v.ChatMode.Mode != "mission" {
		t.Fatalf("a refused change moved the mode to %q", v.ChatMode.Mode)
	}
	// writing the mode it has is no change
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "mission"); code != 0 {
		t.Error("the same mode was refused")
	}
}

func TestChatModeRefusals(t *testing.T) {
	f := newToggleFixture(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--chat-mode", "open"}, "not available yet"},
		{[]string{"--chat-mode", "doomsday"}, "--chat-mode is"},
		{[]string{"--chat-mode", "mission", "--mission", "on"}, "not both"},
		{[]string{"--patch", `{"chat_mode":"open"}`}, "not available yet"},
		{[]string{"--patch", `{"chat_mode":"open","mission":true}`}, "disagree"},
	} {
		out, code := runSessionsCLI(t, f.cfg, append([]string{"settings", "set", f.conv.ID}, c.args...)...)
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("%v: exit %d %s, want a refusal naming %q", c.args, code, out, c.want)
		}
	}
	if v, _, _ := f.run(t, f.cfg, "settings", "get", f.conv.ID); v.ChatMode.Mode != "off" {
		t.Errorf("a refusal changed the mode to %q", v.ChatMode.Mode)
	}
	// open is selectable only when the availability function says so
	old := coderhost.OpenModeAvailability
	coderhost.OpenModeAvailability = func() (bool, string) { return true, "" }
	t.Cleanup(func() { coderhost.OpenModeAvailability = old })
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "open"); code != 0 || v.ChatMode.Mode != "open" {
		t.Errorf("open while available: exit %d %+v", code, v.ChatMode)
	}
}

func TestChatModeNeedsMissionAccess(t *testing.T) {
	dbPath := newChatCLITestDB(t)
	conv, err := openTestChatStore(t, dbPath).CreateConversationAccess("default", "agent", "general", "claude", "", "", ai.AccessScoped, "", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testDeps(&testConfig{DBPath: dbPath, ProfileID: "default", JSONOutput: true})
	// a scoped chat is not a coder conversation: the command refuses before the mode
	if out, code := runSessionsCLI(t, cfg, "settings", "set", conv.ID, "--chat-mode", "mission"); code == 0 {
		t.Errorf("mission on a scoped chat: %s", out)
	}
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := guardChatMode(t.Context(), db.DB, "default", ai.Conversation{ID: conv.ID, Access: ai.AccessScoped}, "mission"); err == nil {
		t.Error("mission below mission access was allowed")
	}
}

func TestChatModeAliasMissionMapsToTheMode(t *testing.T) {
	f := newToggleFixture(t)
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "on"); code != 0 || v.ChatMode.Mode != "mission" || strings.Join(v.Changed, ",") != "mission" {
		t.Fatalf("--mission on: exit %d %+v %v", code, v.ChatMode, v.Changed)
	}
	if v, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--mission", "off"); code != 0 || v.ChatMode.Mode != "off" {
		t.Fatalf("--mission off: exit %d %+v", code, v.ChatMode)
	}
}

func TestHistoryCreateSetsTheChatModeWithTheConversation(t *testing.T) {
	r := newAccessRig(t, true)
	setCoderSettings(t, r.dbPath, coderSettings{Enabled: true})
	out, code := createAs(t, r.dbPath, true, "--access", "full", "--new-workspace", "--chat-mode", "mission")
	if code != 0 {
		t.Fatalf("create with a mode: exit %d %s", code, out)
	}
	var c ai.ConversationRecord
	decodeChatJSON(t, out, &c)
	cfg := testDeps(&testConfig{DBPath: r.dbPath, ProfileID: "default", JSONOutput: true})
	sout, scode := runSessionsCLI(t, cfg, "settings", "get", c.ID)
	var v sessionSettingsView
	decodeChatJSON(t, sout, &v)
	if scode != 0 || v.ChatMode.Mode != "mission" || v.ChatMode.Locked {
		t.Fatalf("the new chat: exit %d %+v", scode, v.ChatMode)
	}
	count := func() int {
		list, _, err := r.store.ListConversations("default", "", 100)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	before := count()
	for _, args := range [][]string{
		{"--access", "scoped", "--chat-mode", "mission"}, {"--access", "full", "--new-workspace", "--chat-mode", "open"}, {"--access", "full", "--new-workspace", "--chat-mode", "x"},
	} {
		if out, code := createAs(t, r.dbPath, true, args...); code == 0 {
			t.Errorf("%v was accepted: %s", args, out)
		}
	}
	if got := count(); got != before {
		t.Errorf("a refused create left %d conversation(s) behind", got-before)
	}
	// a chat made without the flag is off
	out, _ = createAs(t, r.dbPath, true, "--access", "full", "--new-workspace")
	decodeChatJSON(t, out, &c)
	sout, _ = runSessionsCLI(t, cfg, "settings", "get", c.ID)
	decodeChatJSON(t, sout, &v)
	if v.ChatMode.Mode != "off" {
		t.Errorf("default mode = %q", v.ChatMode.Mode)
	}
}

func TestSessionsListShowsTheChatMode(t *testing.T) {
	f := newToggleFixture(t)
	if _, _, code := f.run(t, f.cfg, "settings", "set", f.conv.ID, "--chat-mode", "mission"); code != 0 {
		t.Fatal("set")
	}
	out, code := runSessionsCLI(t, f.cfg, "list")
	if code != 0 || !strings.Contains(out, `"chatMode":"mission"`) && !strings.Contains(out, `"chatMode": "mission"`) {
		t.Fatalf("list: exit %d %s", code, out)
	}
	text := testDeps(&testConfig{DBPath: f.dbPath, ProfileID: "default"})
	if out, code := runSessionsCLI(t, text, "list"); code != 0 || !strings.Contains(out, "mode=mission") {
		t.Fatalf("text list: exit %d %s", code, out)
	}
}

func TestChatModesListsTheChoicesAtAnAccessLevel(t *testing.T) {
	f := newToggleFixture(t)
	type resp struct {
		Options []chatModeOption `json:"options"`
	}
	get := func(access string) map[string]chatModeOption {
		out, code := runSessionsCLI(t, f.cfg, "chat-modes", "--access", access)
		var r resp
		if code != 0 {
			t.Fatalf("chat-modes %s: exit %d %s", access, code, out)
		}
		decodeChatJSON(t, out, &r)
		m := map[string]chatModeOption{}
		for _, o := range r.Options {
			m[o.Mode] = o
		}
		return m
	}
	if m := get("full"); !m["off"].Available || !m["mission"].Available || m["open"].Available || m["open"].Reason != "not available yet" {
		t.Errorf("full: %+v", m)
	}
	if m := get("scoped"); !m["off"].Available || m["mission"].Available || !strings.Contains(m["mission"].Reason, "coding, workspace or full") {
		t.Errorf("scoped: %+v", m)
	}
	if _, code := runSessionsCLI(t, f.cfg, "chat-modes", "--access", "root"); code == 0 {
		t.Error("an unknown level was accepted")
	}
}
