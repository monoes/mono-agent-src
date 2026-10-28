package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/monoes/mono-agent/internal/profiledir"
)

// A command for profile A reaches the browser that runs A and no other,
// however the sets overlap; a browser that runs only other profiles stays
// silent and the caller gets the "no browser set up" error.
func TestACommandNeverReachesABrowserWithoutItsProfile(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a", "p-x")
	waitBrowsers(t, srv, 1)
	b := dialBrowserSet(t, wsURL, instB, "p-b")
	waitBrowsers(t, srv, 2)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Profile: "p-a"}, &Command{Type: CmdCreateTab}, 5*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, map[string]any{"tabId": 1}); cmd == nil || cmd.Type != CmdCreateTab {
		t.Fatalf("A got %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	expectSilence(t, b, 200*time.Millisecond)

	// p-q is in no set and there is no default browser: refused, nobody asked.
	if _, err := srv.SendCommandTo(Target{Profile: "p-q"}, &Command{Type: CmdCreateTab}, time.Second); err == nil {
		t.Fatal("a profile with no browser must not be run in a browser of other profiles")
	}
	expectSilence(t, a, 200*time.Millisecond)
	expectSilence(t, b, 200*time.Millisecond)
}

// Removing a profile from a browser stops routing it there at once, before
// the extension has answered, and a refusal by the extension puts it back.
func TestUnbindTakesEffectBeforeTheExtensionAnswers(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, &Command{
			Type: CmdSetBinding, Params: map[string]interface{}{"profile": "p-b", "profiles": []string{"p-b"}},
		}, 5*time.Second)
		errc <- err
	}()
	// The command is on the wire; the extension has not answered.
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := a.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var cmd Command
	if err := json.Unmarshal(msg, &cmd); err != nil {
		t.Fatal(err)
	}
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-b"}) {
		t.Fatalf("while the unbind is in flight A runs %v, want only p-b", got)
	}
	if _, err := srv.ResolveTarget(Target{Profile: "p-a"}); err == nil {
		t.Fatal("p-a still routes to the browser it is being removed from")
	}
	// The extension refuses: nothing was stored, the old set is back.
	if err := a.WriteJSON(Response{ID: cmd.ID, Success: false, Error: "no"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err == nil {
		t.Fatal("a refused set_binding must be an error")
	}
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a", "p-b"}) {
		t.Fatalf("after the refusal A runs %v, want p-a, p-b", got)
	}
}

// An addition waits for the extension's answer: the bridge does not route a
// profile to a browser that has not confirmed it.
func TestAddedProfileRoutesOnlyAfterTheExtensionConfirms(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, &Command{
			Type: CmdSetBinding, Params: map[string]interface{}{"profile": "p-a", "profiles": []string{"p-a", "p-n"}},
		}, 5*time.Second)
		errc <- err
	}()
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := a.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var cmd Command
	_ = json.Unmarshal(msg, &cmd)
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a"}) {
		t.Fatalf("before the answer A runs %v, want only p-a", got)
	}
	_ = a.WriteJSON(Response{ID: cmd.ID, Success: true})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a", "p-n"}) {
		t.Fatalf("after the answer A runs %v", got)
	}
}

// What a browser claims is cleaned: path-like and blank ids dropped,
// duplicates folded, the set capped.
func TestClaimedSetIsValidatedAndCapped(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	claim := []string{"p-a", "p-a", "", "  ", "../x", "a/b", `a\b`, "p-b"}
	for i := 0; i < MaxProfilesPerBrowser+10; i++ {
		claim = append(claim, fmt.Sprintf("q-%d", i))
	}
	dialBrowserSet(t, wsURL, instA, claim...)
	got := waitBrowsers(t, srv, 1)[0].ProfileIDs
	if len(got) != MaxProfilesPerBrowser || got[0] != "p-a" || got[1] != "p-b" {
		t.Fatalf("set = %v (len %d), want p-a, p-b, then capped at %d", got, len(got), MaxProfilesPerBrowser)
	}
	for _, id := range got {
		if id == "" || id == "../x" || id == "a/b" {
			t.Fatalf("invalid id %q kept", id)
		}
	}
}

// Profiles that no longer exist leave the set; the live ones stay. This
// holds for a binding frame sent later as well as for the auth frame.
func TestDeletedProfilesLeaveTheSet(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	srv.SetProfileSource(func(context.Context) ([]profiledir.Profile, error) {
		return []profiledir.Profile{{ID: "p-live"}, {ID: "p-live2"}}, nil
	})
	a := dialBrowserSet(t, wsURL, instA, "p-gone", "p-live")
	waitBrowsers(t, srv, 1)
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-live"}) {
		t.Fatalf("after connect: %v", got)
	}
	if err := a.WriteJSON(map[string]any{"kind": KindBinding, "profiles": []string{"p-live2", "p-ghost"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reflect.DeepEqual(srv.Browsers()[0].ProfileIDs, []string{"p-live2"}) })
}

// A browser bound to exactly one profile behaves as before sets: its
// profile goes to it, anything else to the default browser.
func TestSingleProfileBrowserBehavesAsBefore(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	var _ *websocket.Conn = dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)
	dialBrowserSet(t, wsURL, instB)
	waitBrowsers(t, srv, 2)
	for p, want := range map[string]string{"p-a": instA, "p-other": instB, "": instB} {
		if info, err := srv.ResolveTarget(Target{Profile: p}); err != nil || info.Instance != want {
			t.Errorf("%q -> %q, %v; want %q", p, info.Instance, err, want)
		}
	}
}
