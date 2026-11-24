package extension

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// askOver sends one request frame on conn and returns the settling reply.
func askOver(t *testing.T, conn *websocket.Conn, id, method string, params map[string]any) Reply {
	t.Helper()
	if err := conn.WriteJSON(Request{Kind: KindRequest, ID: id, Method: method, Params: params}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_ = conn.SetReadDeadline(deadline)
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read reply: %v", err)
		}
		var r Reply
		if json.Unmarshal(msg, &r) == nil && r.Kind == KindReply && r.ID == id && r.Progress == nil {
			return r
		}
	}
}

func TestRequestNamingAProfileOutsideTheAllowedSetIsRefused(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	ran := make(chan string, 4)
	srv.HandleRequest("probe.write", func(_ context.Context, req *Request, _ ProgressFunc) (any, error) {
		ran <- req.String("profile")
		return map[string]any{"ok": true}, nil
	})
	tok, _ := mintRestricted(t, instA, "p-a")
	conn := dialToken(t, wsURL, tok, instA, "p-a")
	waitBrowsers(t, srv, 1)

	if r := askOver(t, conn, "1", "probe.write", map[string]any{"profile": "p-b"}); r.OK || r.Code != CodeForbidden {
		t.Fatalf("a request naming profile p-b was served: %+v", r)
	}
	if r := askOver(t, conn, "2", "probe.write", nil); r.OK || r.Code != CodeForbidden {
		t.Fatalf("a profile-less request from a restricted browser was served: %+v", r)
	}
	if r := askOver(t, conn, "3", "probe.write", map[string]any{"profile": "p-a"}); !r.OK {
		t.Fatalf("a request naming the allowed profile was refused: %+v", r)
	}
	if r := askOver(t, conn, "4", MethodPing, nil); !r.OK {
		t.Fatalf("ping must stay open to a restricted browser: %+v", r)
	}
	if got := <-ran; got != "p-a" {
		t.Fatalf("handler ran for %q, want only p-a", got)
	}
	select {
	case extra := <-ran:
		t.Fatalf("handler also ran for %q", extra)
	default:
	}
}

func TestRequestFromAnUnrestrictedBrowserNamesAnyProfile(t *testing.T) {
	srv, _, wsURL, _ := startStatusTestServer(t)
	srv.HandleRequest("probe.write", func(context.Context, *Request, ProgressFunc) (any, error) { return 1, nil })
	tok := mintPaired(t, instA)
	conn := dialToken(t, wsURL, tok, instA)
	waitBrowsers(t, srv, 1)
	if r := askOver(t, conn, "1", "probe.write", map[string]any{"profile": "p-b"}); !r.OK {
		t.Fatalf("unrestricted browser refused: %+v", r)
	}
}
