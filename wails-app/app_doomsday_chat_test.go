package main

import (
	"encoding/json"
	"strings"
	"testing"
)

const newChatSpec = `{"runtime":"claude","model":"","effort":"","access":"full","cwd":"/tmp/work"}`

func (g *grantRig) renderFor(charter, chatID, spec string) string {
	g.t.Helper()
	var d struct {
		Token string `json:"start_token"`
	}
	if json.Unmarshal([]byte(g.a.DoomsdayRender(charter, chatID, spec)), &d) != nil || d.Token == "" {
		g.t.Fatal("no token")
	}
	return d.Token
}

// The token covers the chat: a preview made for one chat cannot start a run for another, or for a chat to be made, and nothing
// is spent or shown when it is tried.
func TestTheStartTokenIsBoundToTheChat(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	tok := g.renderFor(dmCharter, "chat-A", "")
	calls := g.calls()
	for name, c := range map[string]struct{ id, spec string }{"other chat": {"chat-B", ""}, "a new chat": {"", newChatSpec}} {
		d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, c.id, c.spec))
		if d.OK || d.Code != "token" || g.dialogs != 0 || g.calls() != calls {
			t.Errorf("%s: %+v dialogs=%d", name, d, g.dialogs)
		}
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "chat-A", "")); !d.OK { // the token was not spent by the refusals
		t.Fatalf("the right chat: %+v", d)
	}
}

func TestTheDialogNamesTheChat(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`echo '{"run_id":"work/1"}'`)
	g.a.DoomsdayStart(dmCharter, gPhrase, g.renderFor(dmCharter, "chat-A", ""), "chat-A", "")
	if len(g.asked) != 1 || !strings.Contains(g.asked[0], "the chat chat-A") {
		t.Fatalf("dialog: %q", g.asked)
	}
}

// A chat to be made is made only after the person said yes; a decline or a wrong phrase makes none.
func TestAChatToBeMadeIsMadeOnlyAfterYes(t *testing.T) {
	g := newGrantRig(t)
	g.startBehaves(`case "$*" in *"history create"*|*"create --workflow"*) echo '{"id":"new-chat-1","runtimeId":"claude","access":"full","cwd":"/tmp/work","mode":"coder"}';; *) echo '{"run_id":"work/1"}';; esac`)
	created := func() bool { return strings.Contains(readFile(g.t, g.log), "create|") }
	tok := g.renderFor(dmCharter, "", newChatSpec)
	g.answer = false
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, gPhrase, tok, "", newChatSpec)); d.Code != "declined" || created() {
		t.Fatalf("declined: %+v created=%v", d, created())
	}
	if d := parseStartDoc(t, g.a.DoomsdayStart(dmCharter, "wrong phrase", tok, "", newChatSpec)); d.Code != "phrase" || created() {
		t.Fatalf("mistyped: %+v created=%v", d, created())
	}
	if !strings.Contains(g.asked[0], "a NEW chat on claude") {
		t.Fatalf("dialog: %q", g.asked[0])
	}
	g.answer = true
	raw := g.a.DoomsdayStart(dmCharter, gPhrase, tok, "", newChatSpec)
	var d struct {
		OK   bool `json:"ok"`
		Chat struct {
			ID string `json:"id"`
		} `json:"chat"`
	}
	if json.Unmarshal([]byte(raw), &d) != nil || !d.OK || d.Chat.ID != "new-chat-1" || !created() {
		t.Fatalf("confirmed: %s created=%v", raw, created())
	}
	if b := readFile(t, g.log); !strings.Contains(b, "--chat=new-chat-1") {
		t.Fatalf("the run was not started for the new chat: %s", b)
	}
}

func TestParseChatRefusesBadSpecs(t *testing.T) {
	for name, c := range map[string][2]string{
		"both":          {"c", newChatSpec},
		"neither":       {"", ""},
		"unknown field": {"", `{"runtime":"claude","access":"full","x":1}`},
		"scoped":        {"", `{"runtime":"claude","access":"scoped"}`},
		"no runtime":    {"", `{"access":"full"}`},
		"control char":  {"", `{"runtime":"cla\nude","access":"full"}`},
		"trailing":      {"", newChatSpec + ` {}`},
		"not an object": {"", `["x"]`},
	} {
		if _, err := parseChat(c[0], c[1]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
