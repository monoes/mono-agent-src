package extension

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialBrowserSet connects a fake extension >= 1.8.0: it reports the whole
// set in `profiles` and the first id in `profile`.
func dialBrowserSet(t *testing.T, wsURL, instance string, profiles ...string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	tok, err := CurrentToken()
	if err != nil {
		t.Fatalf("CurrentToken: %v", err)
	}
	first := ""
	if len(profiles) > 0 {
		first = profiles[0]
	}
	if err := conn.WriteJSON(authFrame{Type: "auth", Token: tok, Instance: instance, Profile: first, Profiles: profiles}); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	return conn
}

func TestAuthFrameSetBecomesTheBrowsersSet(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a", "p-b", "p-a", "../x")
	got := waitBrowsers(t, srv, 1)[0]
	if want := []string{"p-a", "p-b"}; !reflect.DeepEqual(got.ProfileIDs, want) {
		t.Fatalf("profile_ids = %v, want %v (deduped, invalid dropped)", got.ProfileIDs, want)
	}
	if got.Profile != "p-a" {
		t.Fatalf("profile = %q, want the first id for old CLIs", got.Profile)
	}
}

func TestOldExtensionProfileIsAOneElementSet(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowser(t, wsURL, instA, "p-a", "") // sends only `profile`
	got := waitBrowsers(t, srv, 1)[0]
	if want := []string{"p-a"}; !reflect.DeepEqual(got.ProfileIDs, want) {
		t.Fatalf("profile_ids = %v, want %v", got.ProfileIDs, want)
	}
	if !got.RunsProfile("p-a") || got.RunsProfile("p-b") {
		t.Fatalf("RunsProfile wrong for %+v", got)
	}
}

func TestOlderBridgeInfoStillReadsAsASet(t *testing.T) {
	// A ConnInfo from a bridge that predates sets has only `profile`.
	old := ConnInfo{Instance: instA, Profile: "p-a"}
	if want := []string{"p-a"}; !reflect.DeepEqual(old.ProfileSet(), want) {
		t.Fatalf("ProfileSet = %v, want %v", old.ProfileSet(), want)
	}
	if got := (ConnInfo{Instance: instA}).ProfileSet(); len(got) != 0 {
		t.Fatalf("unbound ProfileSet = %v", got)
	}
}

func TestSeveralProfilesShareOneBrowser(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	for _, p := range []string{"p-a", "p-b"} {
		info, err := srv.ResolveTarget(Target{Profile: p})
		if err != nil || info.Instance != instA {
			t.Fatalf("%s: got %q, %v; want A", p, info.Instance, err)
		}
	}
	// A browser that runs only other profiles is never used for p-c.
	var nb *NoBrowserError
	_, err := srv.ResolveTarget(Target{Profile: "p-c"})
	if !errors.As(err, &nb) || !reflect.DeepEqual(nb.BoundTo, []string{"p-a", "p-b"}) {
		t.Fatalf("p-c: err = %v, want NoBrowserError naming p-a, p-b", err)
	}
	if got := srv.Browsers()[0]; got.Conflict {
		t.Fatalf("sharing a browser is not a conflict: %+v", got)
	}
}

func TestResolveRulesWithSets(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	dialBrowserSet(t, wsURL, instB)
	waitBrowsers(t, srv, 2)
	dialBrowserSet(t, wsURL, instC, "p-c")
	waitBrowsers(t, srv, 3)

	cases := []struct {
		name string
		t    Target
		want string
	}{
		{"profile in a multi-profile set", Target{Profile: "p-b"}, instA},
		{"profile alone in its browser", Target{Profile: "p-c"}, instC},
		{"unlisted profile falls to the unbound browser", Target{Profile: "p-z"}, instB},
		{"no profile prefers the unbound browser", Target{}, instB},
	}
	for _, tc := range cases {
		info, err := srv.ResolveTarget(tc.t)
		if err != nil || info.Instance != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.name, info.Instance, err, tc.want)
		}
	}
}

func TestNewestBrowserWinsAndBothAreFlaggedWhenASetOverlaps(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	dialBrowserSet(t, wsURL, instB, "p-b", "p-c")
	got := waitBrowsers(t, srv, 2)
	if !got[0].Conflict || !got[1].Conflict {
		t.Fatalf("both browsers run p-b: %+v", got)
	}
	if info, _ := srv.ResolveTarget(Target{Profile: "p-b"}); info.Instance != instB {
		t.Fatalf("p-b resolves to %q, want the newest (B)", info.Instance)
	}
	if info, _ := srv.ResolveTarget(Target{Profile: "p-a"}); info.Instance != instA {
		t.Fatalf("p-a resolves to %q, want A", info.Instance)
	}
}

func TestSetBindingWithASetUpdatesTheServersView(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, &Command{
			Type: CmdSetBinding, Params: map[string]interface{}{"profile": "p-a", "profiles": []string{"p-a", "p-b"}},
		}, 5*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, map[string]any{"ok": true}); cmd == nil || cmd.Type != CmdSetBinding {
		t.Fatalf("A got %+v", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := srv.Browsers()[0]; !reflect.DeepEqual(got.ProfileIDs, []string{"p-a", "p-b"}) {
		t.Fatalf("after set_binding: %+v", got)
	}
}

func TestBindingFrameWithASetUpdatesTheServersView(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA)
	waitBrowsers(t, srv, 1)
	frame := map[string]any{"kind": KindBinding, "profile": "p-q", "profiles": []string{"p-q", "p-r"}, "label": "Shared"}
	if err := a.WriteJSON(frame); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reflect.DeepEqual(srv.Browsers()[0].ProfileIDs, []string{"p-q", "p-r"}) })

	// An extension that clears the set sends profile "" and profiles [].
	if err := a.WriteJSON(map[string]any{"kind": KindBinding, "profile": "", "profiles": []string{}, "label": "Shared"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(srv.Browsers()[0].ProfileIDs) == 0 })

	// An older extension's frame carries only profile.
	if err := a.WriteJSON(map[string]string{"kind": KindBinding, "profile": "p-old", "label": "Shared"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reflect.DeepEqual(srv.Browsers()[0].ProfileIDs, []string{"p-old"}) })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
