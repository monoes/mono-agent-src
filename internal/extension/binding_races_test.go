package extension

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/profiledir"
)

func setBindingCmd(profiles ...string) *Command {
	return &Command{Type: CmdSetBinding, Params: map[string]interface{}{"profile": profiles[0], "profiles": profiles}}
}

func cmdID(frame string) string {
	rest := frame[strings.Index(frame, `"id":"`)+6:]
	return rest[:strings.Index(rest, `"`)]
}

// A set_binding that cannot be written puts back what it narrowed.
func TestFailedWriteRestoresTheNarrowedSet(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	c, err := srv.resolve(Target{Instance: instA})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.ws.SetWriteDeadline(time.Now().Add(-time.Second)) // every write now fails
	if _, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-b"), time.Second); err == nil {
		t.Fatal("a command that cannot be written must fail")
	}
	if got := c.boundProfiles(); !reflect.DeepEqual(got, []string{"p-a", "p-b"}) {
		t.Fatalf("after the failed write the browser runs %v, want p-a, p-b", got)
	}
}

// A refusal puts back only what was removed; a change that arrived in the
// meantime (a binding frame from the extension) is kept.
func TestRestoreKeepsAConcurrentChange(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-b"), 5*time.Second)
		errc <- err
	}()
	_ = a.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := a.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	id := cmdID(string(msg))
	// Meanwhile the user adds p-c in the side panel.
	if err := a.WriteJSON(map[string]any{"kind": KindBinding, "profile": "p-b", "profiles": []string{"p-b", "p-c"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reflect.DeepEqual(srv.Browsers()[0].ProfileIDs, []string{"p-b", "p-c"}) })
	if err := a.WriteJSON(Response{ID: id, Success: false, Error: "no"}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err == nil {
		t.Fatal("a refused set_binding must be an error")
	}
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a", "p-b", "p-c"}) {
		t.Fatalf("after the refusal the browser runs %v, want p-a, p-b, p-c", got)
	}
}

// Two set_bindings for one browser run one after the other.
func TestSetBindingsToOneBrowserAreSerialized(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a", "p-b")
	waitBrowsers(t, srv, 1)
	errc := make(chan error, 2)
	send := func(p ...string) {
		_, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd(p...), 5*time.Second)
		errc <- err
	}
	frames := make(chan string, 4)
	go func() {
		for {
			_, m, err := a.ReadMessage()
			if err != nil {
				return
			}
			frames <- string(m)
		}
	}()
	go send("p-b")
	first := <-frames
	go send("p-a")
	select {
	case m := <-frames:
		t.Fatalf("the second set_binding was sent before the first finished: %s", m)
	case <-time.After(300 * time.Millisecond):
	}
	_ = a.WriteJSON(Response{ID: cmdID(first), Success: true})
	second := <-frames
	_ = a.WriteJSON(Response{ID: cmdID(second), Success: true})
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
}

// A confirmed set is mirrored whole only for an extension that holds a set.
func TestOldExtensionMirrorIsCappedAtOneProfile(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserVersion(t, wsURL, instA, "1.5.0", "p-a")
	waitBrowsers(t, srv, 1)
	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA}, setBindingCmd("p-a", "p-x", "p-y"), 5*time.Second)
		errc <- err
	}()
	if cmd := answerNext(t, a, 0, nil); cmd == nil {
		t.Fatal("no command")
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a"}) {
		t.Fatalf("an extension before 1.12 holds one profile, the bridge believes %v", got)
	}
}

// Looking up deleted profiles neither stalls the browser's frames nor
// overwrites a binding that changed while it waited.
func TestDroppingDeletedProfilesDoesNotClobberAConcurrentBinding(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	srv.SetProfileSource(func(ctx context.Context) ([]profiledir.Profile, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return []profiledir.Profile{{ID: "p-a"}, {ID: "p-b"}}, nil
	})
	if err := a.WriteJSON(map[string]any{"kind": KindBinding, "profiles": []string{"p-a", "p-gone"}}); err != nil {
		t.Fatal(err)
	}
	// The source is still answering; the next frame must be read all the same.
	if err := a.WriteJSON(map[string]any{"kind": KindBinding, "profiles": []string{"p-a", "p-b"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return reflect.DeepEqual(srv.Browsers()[0].ProfileIDs, []string{"p-a", "p-b"}) })
	once.Do(func() { close(release) })
	time.Sleep(300 * time.Millisecond)
	if got := srv.Browsers()[0].ProfileIDs; !reflect.DeepEqual(got, []string{"p-a", "p-b"}) {
		t.Fatalf("the browser runs %v, want p-a, p-b", got)
	}
}

func TestOverlongProfileIDsAreDropped(t *testing.T) {
	long := strings.Repeat("p", MaxProfileIDLen+1)
	ok := strings.Repeat("p", MaxProfileIDLen)
	if got := cleanProfiles([]string{long, ok, "p-a"}); !reflect.DeepEqual(got, []string{ok, "p-a"}) {
		t.Fatalf("cleanProfiles = %v", got)
	}
}
