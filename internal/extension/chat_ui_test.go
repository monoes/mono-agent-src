package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeUIChat struct {
	fakeChat
	actionArgs  []string
	syncArgs    []string
	journalSeen map[string]bool
	out         string
}

func (f *fakeUIChat) UIAction(_ context.Context, p, c string, a []byte) (json.RawMessage, error) {
	f.actionArgs = []string{p, c, string(a)}
	return json.RawMessage(f.out), nil
}

func (f *fakeUIChat) UISync(_ context.Context, p, c string) (json.RawMessage, error) {
	f.syncArgs = []string{p, c}
	return json.RawMessage(`{"surfaced":["hil-1"],"resolved":[]}`), nil
}

func (f *fakeUIChat) UIJournal(_ context.Context, p, c string, seen map[string]bool) (ChatUIJournalPage, error) {
	f.journalSeen = seen
	return ChatUIJournalPage{Turns: []ChatJournalTurn{}}, nil
}

func goodAction() map[string]any {
	return map[string]any{"version": "1.0", "action": map[string]any{
		"name": "submit_form", "surfaceId": "s1", "sourceComponentId": "ok", "context": map[string]any{"a": "b"},
	}}
}

func TestChatUIMethodsAdvertisedOnlyWithUIBackend(t *testing.T) {
	has := func(srv *Server, m string) bool {
		for _, x := range srv.RequestMethods() {
			if x == m {
				return true
			}
		}
		return false
	}
	plain := chatSrv(&fakeChat{})
	if has(plain, MethodChatUIAction) || has(plain, MethodChatUISync) {
		t.Fatal("ui methods advertised for a backend without them")
	}
	srv := chatSrv(&fakeUIChat{})
	if !has(srv, MethodChatUIAction) || !has(srv, MethodChatUISync) {
		t.Fatal("ui methods not advertised")
	}
	srv.SetChatBackend(nil)
	if has(srv, MethodChatUIAction) || has(srv, MethodChatUISync) {
		t.Fatal("ui methods still advertised after removal")
	}
}

func TestChatUIActionHappyPath(t *testing.T) {
	f := &fakeUIChat{out: `{"outcome":"accepted","delivered":"next_turn","message":"hi"}`}
	res, err := callChat(t, chatSrv(f), MethodChatUIAction, map[string]any{"profile": "p-work", "conversation": "c1", "action": goodAction()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(res.(json.RawMessage)) != f.out {
		t.Fatalf("result = %s", res)
	}
	if f.actionArgs[0] != "p-work" || f.actionArgs[1] != "c1" || !strings.Contains(f.actionArgs[2], `"submit_form"`) {
		t.Fatalf("args = %q", f.actionArgs)
	}
}

// Several cards sent together go through as one set envelope (#1803).
func TestChatUIActionPassesASet(t *testing.T) {
	f := &fakeUIChat{out: `{"outcome":"accepted","delivered":"answer"}`}
	set := map[string]any{"version": "1.0", "actions": []any{goodAction()["action"], goodAction()["action"]}, "skipped": []any{"s2"}}
	if _, err := callChat(t, chatSrv(f), MethodChatUIAction, map[string]any{"profile": "p-work", "conversation": "c1", "action": set}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.actionArgs[2], `"actions":[`) || !strings.Contains(f.actionArgs[2], `"skipped":["s2"]`) {
		t.Fatalf("args = %q", f.actionArgs)
	}
}

func TestChatUIActionRefusesBadInput(t *testing.T) {
	big := goodAction()
	big["action"].(map[string]any)["context"] = map[string]any{"a": strings.Repeat("x", 60<<10)}
	for name, params := range map[string]map[string]any{
		"no profile":       {"conversation": "c1", "action": goodAction()},
		"bad profile":      {"profile": "../x", "conversation": "c1", "action": goodAction()},
		"no conversation":  {"profile": "p-work", "action": goodAction()},
		"bad conversation": {"profile": "p-work", "conversation": "--x y", "action": goodAction()},
		"not an object":    {"profile": "p-work", "conversation": "c1", "action": "nope"},
		"not an action":    {"profile": "p-work", "conversation": "c1", "action": map[string]any{"version": "1.0"}},
		"too big":          {"profile": "p-work", "conversation": "c1", "action": big},
		"an empty set":     {"profile": "p-work", "conversation": "c1", "action": map[string]any{"version": "1.0", "actions": []any{}}},
	} {
		f := &fakeUIChat{out: `{}`}
		_, err := callChat(t, chatSrv(f), MethodChatUIAction, params, nil)
		if errCode(err) != CodeInvalidInput {
			t.Errorf("%s: err = %v", name, err)
		}
		if f.actionArgs != nil {
			t.Errorf("%s: the backend ran", name)
		}
	}
}

func TestChatUISync(t *testing.T) {
	f := &fakeUIChat{}
	res, err := callChat(t, chatSrv(f), MethodChatUISync, map[string]any{"profile": "p-work", "conversation": "c1"}, nil)
	if err != nil || !strings.Contains(string(res.(json.RawMessage)), "hil-1") {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if _, err := callChat(t, chatSrv(f), MethodChatUISync, map[string]any{"conversation": "c1"}, nil); errCode(err) != CodeInvalidInput {
		t.Fatalf("no profile: %v", err)
	}
}

func TestCLIBackendUIArgv(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	bin, log := fakeCLI(t, `case "$*" in *" ui action "*) cat > '`+stdin+`';; esac; echo '{"outcome":"accepted"}'`)
	b := &cliChatBackend{bin: bin}
	out, err := b.UIAction(context.Background(), "p-work", "c1", []byte(`{"version":"1.0"}`))
	if err != nil || string(out) != `{"outcome":"accepted"}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
	if _, err := b.UISync(context.Background(), "p-work", "c1"); err != nil {
		t.Fatal(err)
	}
	lines := readLog(t, log)
	if lines[0] != "--profile=p-work --json chat ui action --action - -- c1" {
		t.Fatalf("action argv = %q", lines[0])
	}
	if got, err := os.ReadFile(stdin); err != nil || string(got) != `{"version":"1.0"}` {
		t.Fatalf("action stdin = %q err=%v", got, err)
	}
	if lines[1] != "--profile=p-work --json chat ui sync-hil -- c1" {
		t.Fatalf("sync argv = %q", lines[1])
	}
}

// The real CLI indents its documents over many lines (captured from `chat ui sync-hil`).
func TestCLIBackendUIReadsIndentedOutput(t *testing.T) {
	bin, _ := fakeCLI(t, `printf '{\n  "surfaced": [\n    "hil-482"\n  ],\n  "resolved": []\n}\n'`)
	out, err := (&cliChatBackend{bin: bin}).UISync(context.Background(), "p-work", "c1")
	if err != nil || string(out) != `{"surfaced":["hil-482"],"resolved":[]}` {
		t.Fatalf("out=%s err=%v", out, err)
	}
}

// The cards come with their events from one read, newest first.
const journalCLI = `
case "$*" in
*history\ turns*--cards*--skip=system-uisurface-b*) echo '{"items":[{"id":"system-uiaction-a","events":[{"seq":1,"type":"ui.action","at":"2026-01-01T00:00:00.200Z","payload":{}}]}]}';;
*history\ turns*--cards*) echo '{"items":[{"id":"system-uiaction-a","events":[{"seq":1,"type":"ui.action","at":"2026-01-01T00:00:00.200Z","payload":{}}]},{"id":"system-uisurface-b","events":[{"seq":1,"type":"ui.surface","at":"2026-01-01T00:00:00.100Z","payload":{}}]}]}';;
*history\ turns*) echo '{"items":[{"id":"ext-t-1"}]}';;
*ext-t-1*) echo '{"items":[{"seq":3,"type":"notice","payload":{}}],"turn":{"status":"completed"}}';;
esac
`

// However many card turns a chat holds, a journal read is one CLI run.
func TestCLIBackendJournalReadsTheCardsInOneRun(t *testing.T) {
	items := make([]string, 60)
	for i := range items { // newest first, as the CLI lists them
		items[i] = fmt.Sprintf(`{"id":"system-uisurface-%02d","events":[{"seq":1,"type":"ui.surface","at":"2026-01-01T00:%02d:00Z","payload":{}}]}`, 59-i, 59-i)
	}
	file := filepath.Join(t.TempDir(), "page.json")
	if err := os.WriteFile(file, []byte(`{"items":[`+strings.Join(items, ",")+`]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bin, log := fakeCLI(t, `cat '`+file+`'`)
	page, err := (&cliChatBackend{bin: bin}).UIJournal(context.Background(), "p-work", "c1", map[string]bool{"system-uisurface-00": true})
	if err != nil || len(page.Turns) != chatJournalPerCall || !page.More || page.Turns[0].Turn != "system-uisurface-01" || len(page.Turns[0].Events) != 1 {
		t.Fatalf("%d turns, more %v, first %+v, %v", len(page.Turns), page.More, page.Turns[:1], err)
	}
	if lines := readLog(t, log); len(lines) != 1 || lines[0] != "--profile=p-work --json chat history turns --limit=200 --cards --events --skip=system-uisurface-00 -- c1" {
		t.Fatalf("runs %q", lines)
	}
}

func TestCLIBackendJournalTurns(t *testing.T) {
	bin, _ := fakeCLI(t, journalCLI)
	b := &cliChatBackend{bin: bin}
	ctx := context.Background()
	// "The latest turn" passes over the cards' own turns.
	latest, err := b.ListEvents(ctx, "p-work", "c1", "", 0)
	if err != nil || latest.Turn != "ext-t-1" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	// All of them, in the order they were journaled (not the list's id order).
	page, err := b.UIJournal(ctx, "p-work", "c1", nil)
	if err != nil || len(page.Turns) != 2 || page.Turns[0].Turn != "system-uisurface-b" || page.Turns[1].Turn != "system-uiaction-a" || page.More {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Turns[0].Events[0].At == "" {
		t.Fatal("the events lost their time")
	}
	page, err = b.UIJournal(ctx, "p-work", "c1", map[string]bool{"system-uisurface-b": true})
	if err != nil || len(page.Turns) != 1 || page.Turns[0].Turn != "system-uiaction-a" {
		t.Fatalf("unseen=%+v err=%v", page, err)
	}
}

func TestChatUIJournalParams(t *testing.T) {
	f := &fakeUIChat{}
	srv := chatSrv(f)
	ok := map[string]any{"profile": "p-work", "conversation": "c1", "seen": []any{"system-uiaction-a"}}
	if _, err := callChat(t, srv, MethodChatUIJournal, ok, nil); err != nil || !f.journalSeen["system-uiaction-a"] {
		t.Fatalf("err=%v seen=%v", err, f.journalSeen)
	}
	for name, seen := range map[string]any{"not a list": "x", "bad id": []any{"-- x"}, "number": []any{1}, "too many": make([]any, 201)} {
		_, err := callChat(t, srv, MethodChatUIJournal, map[string]any{"profile": "p-work", "conversation": "c1", "seen": seen}, nil)
		if errCode(err) != CodeInvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := callChat(t, srv, MethodChatUIJournal, map[string]any{"conversation": "c1"}, nil); errCode(err) != CodeInvalidInput {
		t.Errorf("no profile: %v", err)
	}
}
