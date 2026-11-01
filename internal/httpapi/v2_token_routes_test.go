package httpapi

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/procbind"
)

// v2Secrets stores a genuine v2 mission token and a genuine v2 session token (rows, bindings and a host start all
// valid) in the server's database and returns their secrets. Whatever a route did with them, they are the strongest
// v2 credential there is.
func v2Secrets(t *testing.T, s *Server, profile string) []string {
	t.Helper()
	ctx := context.Background()
	key, err := procbind.RowKey(ctx, s.rt.db.DB, profile)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := procbind.InsertHost(ctx, s.rt.db.DB, key, profile, "gen-1", now); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, kind := range []procbind.Kind{procbind.KindMission, procbind.KindSession} {
		prefix, tok := procbind.TaskPrefixV2, procbind.MintToken{Kind: kind, Scope: "mission:1:1", Owner: "o", Role: "worker", Actor: "a", Session: 1, Task: 1}
		if kind == procbind.KindSession {
			prefix, tok = procbind.SessionPrefixV2, procbind.MintToken{Kind: kind, Scope: "session:c/t", ConversationID: "c", TurnID: "t"}
		}
		secret, hash, err := procbind.NewSecret(prefix)
		if err != nil {
			t.Fatal(err)
		}
		id, err := procbind.NewTokenID()
		if err != nil {
			t.Fatal(err)
		}
		tok.ID, tok.Hash, tok.Expires = id, hash, now.Add(time.Hour)
		if err := procbind.MintTx(ctx, s.rt.db.DB, key, procbind.MintRequest{Profile: profile, WorkerPID: os.Getpid(), WorkerStamp: "s", HostGeneration: "gen-1", Now: now, Tokens: []procbind.MintToken{tok}}); err != nil {
			t.Fatal(err)
		}
		out = append(out, secret)
	}
	return out
}

var pathParam = regexp.MustCompile(`\{[^}]*\}`)

// concretePath turns a registered pattern into a request path.
func concretePath(pattern string) string {
	_, p, _ := strings.Cut(pattern, " ")
	return pathParam.ReplaceAllStringFunc(p, func(m string) string {
		if m == "{profile}" {
			return "default"
		}
		if m == "{$}" {
			return ""
		}
		return "x"
	})
}

// The HTTP bearer routes read only agent_tokens and the profile's own bearer: no route accepts a v2 token. The test
// reads the router's own table, so a route added later is presented a v2 token too, with no list to forget.
func TestNoHTTPRouteAcceptsAV2Token(t *testing.T) {
	s := newTestServer(t, true)
	if len(s.routeTable) == 0 {
		t.Fatal("the router registered nothing")
	}
	secrets := v2Secrets(t, s, "default")
	tried := 0
	for _, rc := range s.routeTable {
		if rc.Open {
			continue
		}
		method, _, _ := strings.Cut(rc.Pattern, " ")
		path := concretePath(rc.Pattern)
		for _, secret := range secrets {
			rw := doReq(t, s, method, path, secret, []byte(`{}`))
			tried++
			if rw.Code != http.StatusUnauthorized {
				t.Errorf("%s with a v2 token (%s...): status %d, want 401; %s", rc.Pattern, secret[:5], rw.Code, rw.Body)
			}
		}
	}
	if tried == 0 {
		t.Fatal("no route was tried")
	}
}
