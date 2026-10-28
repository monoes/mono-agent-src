package extension

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRemoteSenderRoutesByProfile(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	a := dialBrowser(t, wsURL, instA, "p-a", "")
	waitBrowsers(t, srv, 1)
	b := dialBrowser(t, wsURL, instB, "p-b", "")
	waitBrowsers(t, srv, 2)

	r := NewRemoteSender(base)
	done := make(chan int, 1)
	go func() {
		id, err := r.CreateTabFor(Target{Profile: "p-b"}, "https://example.com")
		if err != nil {
			t.Errorf("CreateTabFor: %v", err)
		}
		done <- id
	}()
	if cmd := answerNext(t, b, 0, map[string]any{"tabId": 9}); cmd == nil || cmd.Type != CmdCreateTab {
		t.Fatalf("B got %+v", cmd)
	}
	if id := <-done; id != 9 {
		t.Fatalf("tab = %d", id)
	}
	expectSilence(t, a, 200*time.Millisecond)
}

func TestRemoteResolveAndBrowsers(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	r := NewRemoteSender(base)
	if _, err := r.ResolveTarget(Target{Profile: "p-a"}); !errors.Is(err, ErrNoExtension) {
		t.Fatalf("empty bridge: err = %v, want ErrNoExtension", err)
	}

	dialBrowser(t, wsURL, instA, "p-a", "Edge Work")
	waitBrowsers(t, srv, 1)
	dialBrowser(t, wsURL, instB, "p-b", "")
	waitBrowsers(t, srv, 2)

	info, err := r.ResolveTarget(Target{Profile: "p-a"})
	if err != nil || info.Instance != instA || info.Label != "Edge Work" {
		t.Fatalf("resolve p-a = %+v, %v", info, err)
	}
	_, err = r.ResolveTarget(Target{Profile: "p-x"})
	var nb *NoBrowserError
	if !errors.As(err, &nb) || nb.Profile != "p-x" || strings.Join(nb.BoundTo, ",") != "p-a,p-b" {
		t.Fatalf("resolve p-x err = %v", err)
	}
	if _, err := r.ResolveTarget(Target{Instance: instC}); !errors.Is(err, ErrBrowserGone) {
		t.Fatalf("resolve gone instance err = %v", err)
	}

	list, err := r.Browsers()
	if err != nil || len(list) != 2 {
		t.Fatalf("Browsers = %+v, %v", list, err)
	}
}

func TestRoutingEndpointsNeedTheToken(t *testing.T) {
	_, base, _, _ := startStatusTestServer(t)
	for _, path := range []string{"/monoagent/browsers", "/monoagent/resolve?profile=p-a"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestRemoteResolveOnAnOldBridge(t *testing.T) {
	old := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(old.Close)
	r := &RemoteSender{baseURL: old.URL, client: &http.Client{}}
	if _, err := r.ResolveTarget(Target{Profile: "p-a"}); !errors.Is(err, ErrBridgeTooOld) {
		t.Fatalf("err = %v, want ErrBridgeTooOld", err)
	}
	if _, err := r.Browsers(); !errors.Is(err, ErrBridgeTooOld) {
		t.Fatalf("Browsers err = %v, want ErrBridgeTooOld", err)
	}
}

func TestCdpClientIsPinnedToOneBrowser(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	a := dialBrowser(t, wsURL, instA, "p-a", "")
	waitBrowsers(t, srv, 1)
	b := dialBrowser(t, wsURL, instB, "p-b", "")
	waitBrowsers(t, srv, 2)

	tok, _ := CurrentToken()
	cdpURL := strings.Replace(wsURL, "/monoagent", "/monoagent/cdp?profile=p-b", 1)
	client, _, err := websocket.DefaultDialer.Dial(cdpURL, http.Header{tokenHeader: {tok}})
	if err != nil {
		t.Fatalf("dial cdp: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.WriteJSON(Command{ID: "c1", Type: CmdCdpAttach, TabID: 5}); err != nil {
		t.Fatal(err)
	}
	if cmd := answerNext(t, b, 0, map[string]any{"tabId": 5}); cmd == nil || cmd.Type != CmdCdpAttach {
		t.Fatalf("B got %+v, want cdp_attach", cmd)
	}
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := client.ReadMessage(); err != nil { // the attach reply
		t.Fatalf("attach reply: %v", err)
	}

	// An event from A (same tab id, other browser) must not reach this client.
	_ = a.WriteJSON(Response{Type: CdpEventType, Success: true, Data: map[string]any{"from": "A"}})
	_ = b.WriteJSON(Response{Type: CdpEventType, Success: true, Data: map[string]any{"from": "B"}})
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("event: %v", err)
	}
	var ev struct {
		Data map[string]string `json:"data"`
	}
	_ = json.Unmarshal(msg, &ev)
	if ev.Data["from"] != "B" {
		t.Fatalf("client got %s, want only B's event", msg)
	}
}

// Deleting a profile unbinds its browsers through the remote sender (extensioncli.UnbindProfile):
// list them, then set_binding with an empty profile. The server's view must show them unbound.
func TestRemoteSenderUnbindsABrowser(t *testing.T) {
	srv, base, wsURL, _ := startStatusTestServer(t)
	a := dialBrowser(t, wsURL, instA, "p-a", "Edge Work")
	waitBrowsers(t, srv, 1)
	dialBrowser(t, wsURL, instB, "p-b", "")
	waitBrowsers(t, srv, 2)

	r := NewRemoteSender(base)
	list, err := r.Browsers()
	if err != nil || len(list) != 2 {
		t.Fatalf("Browsers = %+v, %v", list, err)
	}
	errc := make(chan error, 1)
	go func() { errc <- r.SetBinding(instA, nil, "") }()
	cmd := answerNext(t, a, 0, map[string]any{"profile": ""})
	if cmd == nil || cmd.Type != CmdSetBinding || cmd.Params["profile"] != "" {
		t.Fatalf("the browser got %+v, want set_binding with an empty profile", cmd)
	}
	if err := <-errc; err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	bound := map[string]string{}
	for _, b := range srv.Browsers() {
		bound[b.Instance] = b.Profile
	}
	if bound[instA] != "" || bound[instB] != "p-b" {
		t.Fatalf("after unbinding: %v, want a unbound and b still p-b", bound)
	}
}
