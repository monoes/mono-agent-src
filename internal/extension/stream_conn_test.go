package extension

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// streamID waits for the single open capture stream and returns its id.
func streamID(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.pendMu.Lock()
		for id := range srv.streams {
			srv.pendMu.Unlock()
			return id
		}
		srv.pendMu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no capture stream opened")
	return ""
}

// A capture stream belongs to the browser it was sent to: a frame with its id
// from another connection is not part of it.
func TestCaptureStreamIgnoresFramesFromAnotherConnection(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, idA := mintRestricted(t, instA, "p-a")
	_, idB := mintRestricted(t, instGoneB, "p-b")
	_, _ = fakeBrowser(t, srv, idA, instA, "p-a")
	cB, _ := fakeBrowser(t, srv, idB, instGoneB, "p-b")

	errc := make(chan error, 1)
	go func() {
		_, err := srv.CapturePage(CaptureRequest{Inbox: t.TempDir(), Target: Target{Instance: instA, Profile: "p-a"}, Timeout: 700 * time.Millisecond})
		errc <- err
	}()
	id := streamID(t, srv)
	srv.dispatch(cB, &Response{ID: id, Success: false, Error: "forged by the other browser"})

	err := <-errc
	if err == nil || strings.Contains(err.Error(), "forged") || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want the foreign frame ignored and the capture to run on to its own timeout", err)
	}
}

// A browser that disconnects mid-capture fails the capture at once, naming the cause.
func TestCaptureStreamFailsWhenItsBrowserDisconnects(t *testing.T) {
	srv, _, _, _ := startStatusTestServer(t)
	_, id := mintRestricted(t, instA, "p-a")
	_, conn := fakeBrowser(t, srv, id, instA, "p-a")

	errc := make(chan error, 1)
	go func() {
		_, err := srv.CapturePage(CaptureRequest{Inbox: t.TempDir(), Target: Target{Instance: instA, Profile: "p-a"}, Timeout: time.Minute})
		errc <- err
	}()
	streamID(t, srv)
	_ = conn.Close()

	select {
	case err := <-errc:
		if !errors.Is(err, ErrBrowserGone) || !strings.Contains(err.Error(), "disconnected") || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("err = %v, want ErrBrowserGone naming the disconnect", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the capture kept waiting after its browser disconnected")
	}
}
