package extension

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const instGoneB = "bbbbbbbb-0000-4000-8000-00000000000b"

// hookRegistered returns a channel that is closed once the first command has
// registered its waiter and written to the socket (afterWrite runs after both),
// so a test closes the socket only while a waiter is in place.
func hookRegistered(t *testing.T) <-chan struct{} {
	t.Helper()
	registered := make(chan struct{})
	var once sync.Once
	afterWrite = func() { once.Do(func() { close(registered) }) }
	t.Cleanup(func() { afterWrite = nil })
	return registered
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// A browser that dies while a command is in flight fails that command at once,
// naming the cause, instead of leaving the caller to wait out the timeout.
func TestPendingCommandFailsWhenItsBrowserDisconnects(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	_, conn := fakeBrowser(t, srv, id, instA, "p-a")
	registered := hookRegistered(t)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{Type: CmdCreateTab}, time.Minute)
		errc <- err
	}()
	waitSignal(t, registered, "the waiter to register")
	_ = conn.Close()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrBrowserGone) {
			t.Fatalf("err = %v, want ErrBrowserGone", err)
		}
		msg := err.Error()
		if !strings.Contains(msg, "disconnected") || strings.Contains(msg, "timed out") ||
			!strings.Contains(msg, "may or may not have run") || strings.Contains(msg, "run it again once") {
			t.Fatalf("the message must name the disconnect and warn about a repeat: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command kept waiting after its browser disconnected")
	}
}

// Another browser's disconnect does not touch a command in flight elsewhere.
func TestCommandOnAnotherConnectionSurvivesADisconnect(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, idA := mintRestricted(t, instA, "p-a")
	_, idB := mintRestricted(t, instGoneB, "p-b")
	_, connA := fakeBrowser(t, srv, idA, instA, "p-a")
	_, _ = fakeBrowser(t, srv, idB, instGoneB, "p-b")
	registered := hookRegistered(t)

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instGoneB, Profile: "p-b"}, &Command{Type: CmdCreateTab}, 700*time.Millisecond)
		errc <- err
	}()
	waitSignal(t, registered, "the waiter to register")
	_ = connA.Close()

	err := <-errc
	if err == nil || errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want the command on the other browser to run on to its own timeout", err)
	}
}

// A response dispatched just before the socket closes is the answer, not a disconnect.
func TestAnswerArrivingJustBeforeTheCloseWins(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	c, conn := fakeBrowser(t, srv, id, instA, "p-a")

	afterWrite = func() {
		srv.dispatch(c, &Response{ID: "answered-1", Success: true})
		_ = conn.Close()
		select {
		case <-c.gone:
		case <-time.After(5 * time.Second):
			t.Error("the connection never closed")
		}
	}
	t.Cleanup(func() { afterWrite = nil })

	for i := 0; i < 20; i++ { // the select picks at random between two ready cases
		if i > 0 {
			c, conn = fakeBrowser(t, srv, id, instA, "p-a")
		}
		resp, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{ID: "answered-1", Type: CmdCreateTab}, time.Minute)
		if err != nil || resp == nil || !resp.Success {
			t.Fatalf("round %d: resp = %+v, err = %v, want the answer to win", i, resp, err)
		}
	}
}

// A browser that reconnects under the same instance id replaces the old socket:
// a waiter on the old one fails, one on the new one is unaffected.
func TestReplacedConnectionFailsItsWaiterOnly(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	_, _ = fakeBrowser(t, srv, id, instA, "p-a")
	registered := hookRegistered(t)

	oldErr := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{Type: CmdCreateTab}, time.Minute)
		oldErr <- err
	}()
	waitSignal(t, registered, "the waiter to register")
	_, _ = fakeBrowser(t, srv, id, instA, "p-a") // same instance: the old socket is closed

	select {
	case err := <-oldErr:
		if !errors.Is(err, ErrBrowserGone) {
			t.Fatalf("old waiter: err = %v, want ErrBrowserGone", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter on the replaced connection kept waiting")
	}
	_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{Type: CmdCreateTab}, 300*time.Millisecond)
	if err == nil || errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a command on the new connection: err = %v, want its own timeout", err)
	}
}

// Nothing connected: the error names a next step, and is still ErrNoExtension.
func TestNoBrowserConnectedSaysWhatToDoNext(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, err := srv.ResolveTarget(Target{Profile: "p-a"})
	if !errors.Is(err, ErrNoExtension) {
		t.Fatalf("err = %v, want ErrNoExtension", err)
	}
	for _, want := range []string{"no extension connected", "MonoAgent Bridge extension", "monoagentcli extension status", "30 s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q lacks %q", err, want)
		}
	}
}

// A browser IS connected but limited to named profiles: no "open a browser" hint.
func TestProfileLimitedBrowserErrorHasNoOpenABrowserHint(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	_, _ = fakeBrowser(t, srv, id, instA, "p-a")
	_, err := srv.ResolveTarget(Target{})
	if !errors.Is(err, ErrNoExtension) || !strings.Contains(err.Error(), "limited to named profiles") {
		t.Fatalf("err = %v, want the profile-limited ErrNoExtension", err)
	}
	if strings.Contains(err.Error(), "open a browser") || strings.Contains(err.Error(), "30 s") {
		t.Fatalf("a browser is connected, the hint is wrong here: %v", err)
	}
}
