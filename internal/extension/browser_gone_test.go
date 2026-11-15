package extension

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// A browser that dies while a command is in flight fails that command at once,
// naming the cause, instead of leaving the caller to wait out the timeout.
func TestPendingCommandFailsWhenItsBrowserDisconnects(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	_, conn := fakeBrowser(t, srv, id, instA, "p-a")

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: instA, Profile: "p-a"}, &Command{Type: CmdCreateTab}, time.Minute)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond) // the command is written and waiting
	_ = conn.Close()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrBrowserGone) {
			t.Fatalf("err = %v, want ErrBrowserGone", err)
		}
		if !strings.Contains(err.Error(), "disconnected") || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("the message must name the disconnect, not a timeout: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the command kept waiting after its browser disconnected")
	}
}

// Another browser's disconnect does not touch a command in flight elsewhere.
func TestCommandOnAnotherConnectionSurvivesADisconnect(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, idA := mintRestricted(t, instA, "p-a")
	_, idB := mintRestricted(t, "bbbbbbbb-0000-4000-8000-00000000000b", "p-b")
	_, connA := fakeBrowser(t, srv, idA, instA, "p-a")
	_, _ = fakeBrowser(t, srv, idB, "bbbbbbbb-0000-4000-8000-00000000000b", "p-b")

	errc := make(chan error, 1)
	go func() {
		_, err := srv.SendCommandTo(Target{Instance: "bbbbbbbb-0000-4000-8000-00000000000b", Profile: "p-b"}, &Command{Type: CmdCreateTab}, 700*time.Millisecond)
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	_ = connA.Close()

	err := <-errc
	if err == nil || errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want the command on the other browser to run on to its own timeout", err)
	}
}

func TestNoExtensionErrorSaysWhatToDoNext(t *testing.T) {
	msg := ErrNoExtension.Error()
	for _, want := range []string{"no extension connected", "MonoAgent Bridge extension", "monoagentcli extension status", "30 s"} {
		if !strings.Contains(msg, want) {
			t.Errorf("ErrNoExtension %q lacks %q", msg, want)
		}
	}
}
