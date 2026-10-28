package extension

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialWithToken connects a fake extension using the given token and reports
// whether the server closed it with the unauthorized code.
func dialWithToken(t *testing.T, wsURL, token, instance string, profiles ...string) (*websocket.Conn, bool) {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	first := ""
	if len(profiles) > 0 {
		first = profiles[0]
	}
	if err := conn.WriteJSON(authFrame{Type: "auth", Token: token, Instance: instance, Version: "1.12.0", Profile: first, Profiles: profiles}); err != nil {
		t.Fatalf("write auth frame: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
	_, _, err = conn.ReadMessage()
	ce, closed := err.(*websocket.CloseError)
	return conn, closed && ce.Code == unauthorizedCloseCode
}

func mintPaired(t *testing.T, instance string) string {
	t.Helper()
	tok, _, err := addPairedBrowser(PairedBrowser{Label: "b", Instance: instance}, mintOpts{})
	if err != nil {
		t.Fatalf("addPairedBrowser: %v", err)
	}
	return tok
}

// A token paired to browser B cannot connect as browser A: it is refused
// and A's socket, with its profiles, is untouched.
func TestPairedTokenCannotConnectAsAnotherInstance(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)

	tokB := mintPaired(t, instB)
	if _, refused := dialWithToken(t, wsURL, tokB, instA, "p-victim"); !refused {
		t.Fatal("a token paired to instance B was accepted as instance A")
	}
	got := waitBrowsers(t, srv, 1)[0]
	if got.Instance != instA || len(got.ProfileIDs) != 1 || got.ProfileIDs[0] != "p-a" {
		t.Fatalf("A was displaced or rewritten: %+v", got)
	}
	// Its own instance still works.
	if _, refused := dialWithToken(t, wsURL, tokB, instB, "p-b"); refused {
		t.Fatal("a token was refused for its own instance")
	}
	waitBrowsers(t, srv, 2)
}

// A token with no recorded instance may not take an instance another
// paired browser holds, nor replace a socket that authenticated with a
// different per-browser token; the shared token may not either.
func TestConnectionCannotReplaceAnotherTokensSocket(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	tokA := mintPaired(t, instA)
	if _, refused := dialWithToken(t, wsURL, tokA, instA, "p-a"); refused {
		t.Fatal("A refused")
	}
	waitBrowsers(t, srv, 1)

	tokNoInst := mintPaired(t, "")
	if _, refused := dialWithToken(t, wsURL, tokNoInst, instA, "p-victim"); !refused {
		t.Fatal("an instance-less token took a paired instance")
	}
	shared, _ := CurrentToken()
	if _, refused := dialWithToken(t, wsURL, shared, instA, "p-victim"); !refused {
		t.Fatal("the shared token displaced a per-browser socket")
	}
	got := waitBrowsers(t, srv, 1)[0]
	if got.Instance != instA || got.ProfileIDs[0] != "p-a" {
		t.Fatalf("A was displaced: %+v", got)
	}
}

// Pins the documented limit (SECURITY.md, threat-model.md): the relay has no
// operator/agent distinction, so the relay token IS the credential for
// set_binding. Without the token a post is refused and reaches no browser; a
// wrong-token one too. With it, set_binding is forwarded (see
// TestRemoteSenderUnbindsABrowser).
func TestRelaySetBindingNeedsTheRelayToken(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	a := dialBrowserSet(t, wsURL, instA, "p-a")
	waitBrowsers(t, srv, 1)
	for _, tok := range []string{"", "not-the-token"} {
		req, _ := http.NewRequest(http.MethodPost, base+"/monoagent/relay?instance="+instA,
			strings.NewReader(`{"id":"x","type":"set_binding","params":{"profile":"","profiles":[]}}`))
		if tok != "" {
			req.Header.Set(tokenHeader, tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("set_binding with token %q = %d, want 401", tok, resp.StatusCode)
		}
	}
	expectSilence(t, a, 200*time.Millisecond)
	if got := srv.Browsers()[0]; len(got.ProfileIDs) != 1 || got.ProfileIDs[0] != "p-a" {
		t.Fatalf("the binding changed: %+v", got)
	}
}
