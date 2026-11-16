package extension

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The relay carries the server's no-extension wording to the client, and the
// sentinel still matches.
func TestRelayedNoExtensionKeepsTheHint(t *testing.T) {
	_, base, _, _ := startStatusTestServer(t)
	_, err := NewRemoteSender(base).ResolveTarget(Target{Profile: "p-a"})
	if !errors.Is(err, ErrNoExtension) {
		t.Fatalf("err = %v, want ErrNoExtension", err)
	}
	for _, want := range []string{"no extension connected", "MonoAgent Bridge extension", "monoagentcli extension status", "30 s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("relayed %q lacks %q", err, want)
		}
	}
}

// The browser_gone path keeps the sentinel and the server's words.
func TestRelayedBrowserGoneKeepsTheSentinelAndText(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	dialBrowser(t, wsURL, instA, "p-a", "")
	waitBrowsers(t, srv, 1)
	_, err := NewRemoteSender(base).ResolveTarget(Target{Instance: instB})
	if !errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), instB) {
		t.Fatalf("err = %v, want ErrBrowserGone naming the instance", err)
	}
}

// startPending sends a command to the browser c and returns once it is written and waiting.
func startPending(t *testing.T, srv *Server, inst, id string) <-chan *Response {
	t.Helper()
	registered := hookRegistered(t)
	out := make(chan *Response, 1)
	go func() {
		resp, _ := srv.SendCommandTo(Target{Instance: inst, Profile: "p-a"}, &Command{ID: id, Type: CmdCreateTab}, 5*time.Second)
		out <- resp
	}()
	waitSignal(t, registered, "the waiter to register")
	return out
}

// A response from a connection the command was not sent to is ignored; the right one completes it.
func TestResponseFromAnotherConnectionIsIgnored(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, idA := mintRestricted(t, instA, "p-a")
	_, idB := mintRestricted(t, instGoneB, "p-b")
	a, _ := fakeBrowser(t, srv, idA, instA, "p-a")
	b, _ := fakeBrowser(t, srv, idB, instGoneB, "p-b")
	out := startPending(t, srv, instA, "cmd-1")

	srv.dispatch(b, &Response{ID: "cmd-1", Success: true, Data: "from-the-wrong-browser"})
	srv.dispatch(a, &Response{ID: "cmd-1", Success: true, Data: "from-the-right-browser"})
	resp := <-out
	if resp == nil || resp.Data != "from-the-right-browser" {
		t.Fatalf("resp = %+v, want the right browser's answer", resp)
	}
}

// A late answer from a replaced socket does not complete the new socket's command.
func TestLateResponseFromAReplacedSocketIsIgnored(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	old, _ := fakeBrowser(t, srv, id, instA, "p-a")
	cur, _ := fakeBrowser(t, srv, id, instA, "p-a")
	out := startPending(t, srv, instA, "cmd-2")

	srv.dispatch(old, &Response{ID: "cmd-2", Success: true, Data: "late-from-old"})
	srv.dispatch(cur, &Response{ID: "cmd-2", Success: true, Data: "from-new"})
	if resp := <-out; resp == nil || resp.Data != "from-new" {
		t.Fatalf("resp = %+v, want the new socket's answer", resp)
	}
}

// A second command with an id already waiting is refused and does not take over the first one's entry.
func TestDuplicateCommandIDIsRefused(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	a, _ := fakeBrowser(t, srv, id, instA, "p-a")
	out := startPending(t, srv, instA, "dup-1")

	_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{ID: "dup-1", Type: CmdCreateTab}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "already in flight") {
		t.Fatalf("duplicate err = %v, want a refusal", err)
	}
	srv.dispatch(a, &Response{ID: "dup-1", Success: true, Data: "first"})
	if resp := <-out; resp == nil || resp.Data != "first" {
		t.Fatalf("first waiter lost its entry: %+v", resp)
	}
}
