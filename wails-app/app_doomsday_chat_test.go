package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// The dialog says what is made: runtime, model, effort, access and folder, all as they are.
func TestTheNewChatTextShowsEverythingThatIsMade(t *testing.T) {
	c, err := parseChat("", `{"runtime":"codex","model":"gpt-5","effort":"high","access":"workspace","cwd":"/srv/proj"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codex", "model gpt-5", "effort high", "access workspace", "folder /srv/proj"} {
		if !strings.Contains(c.text, want) {
			t.Errorf("dialog text %q lacks %q", c.text, want)
		}
	}
	d, _ := parseChat("", newChatSpec)
	if !strings.Contains(d.text, "model default") || !strings.Contains(d.text, "effort default") {
		t.Errorf("defaults are not shown: %q", d.text)
	}
}

// A start that did not happen removes the chat it made (the guarded delete), so failed attempts pile up no empty chats; one that
// may have started keeps it (the run's boss stands behind it); a delete that fails hands the chat to the page.
func TestAFailedStartRemovesTheChatItMade(t *testing.T) {
	script := func(startBody, deleteBody string) string {
		return `case "$*" in *"create --workflow"*) echo '{"id":"new-chat-1","runtimeId":"claude","access":"full","cwd":"/tmp/work","mode":"coder"}';; *"history delete"*) ` + deleteBody + `;; *) ` + startBody + `;; esac`
	}
	refused := `echo '{"error":"the entry is refused","code":"invalid_input"}'; exit 3`
	for name, c := range map[string]struct {
		start, del string
		wantDelete bool
		wantChat   bool
		wantState  string
	}{
		"refused":               {refused, "echo ok", true, false, "not_started"},
		"refused, delete fails": {refused, "echo no >&2; exit 1", true, true, "not_started"},
		"unknown":               {"sleep 0; kill -9 $$", "echo ok", false, true, "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGrantRig(t)
			g.startBehaves(script(c.start, c.del))
			tok := g.renderFor(dmCharter, "", newChatSpec)
			raw := g.a.DoomsdayStart(dmCharter, gPhrase, tok, "", newChatSpec)
			var d struct {
				State string          `json:"state"`
				Chat  json.RawMessage `json:"chat"`
			}
			_ = json.Unmarshal([]byte(raw), &d)
			deleted := strings.Contains(readFile(t, g.log), "delete|")
			if d.State != c.wantState || deleted != c.wantDelete || (len(d.Chat) > 0) != c.wantChat {
				t.Fatalf("state=%q deleted=%v chat=%s raw=%s", d.State, deleted, d.Chat, raw)
			}
		})
	}
}

// A folder that is, or goes through, a symlink is refused: the dialog would name one place and the chat would work in another.
func TestParseChatRefusesASymlinkedFolder(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here")
	}
	spec := func(p string) string {
		b, _ := json.Marshal(map[string]string{"runtime": "claude", "access": "full", "cwd": p})
		return string(b)
	}
	if _, err := parseChat("", spec(link)); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("the link was accepted: %v", err)
	}
	if _, err := parseChat("", spec(filepath.Join(link, "sub"))); err == nil {
		t.Fatal("a path through the link was accepted")
	}
	if _, err := parseChat("", spec(real)); err != nil {
		t.Fatalf("the real folder was refused: %v", err)
	}
	if _, err := parseChat("", spec(filepath.Join(base, "not-yet"))); err != nil {
		t.Fatalf("a folder that does not exist yet was refused: %v", err)
	}
}
