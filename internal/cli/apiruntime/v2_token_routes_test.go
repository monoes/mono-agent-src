package apiruntime

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/orgbridge"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/procbind"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The routes the HTTP API mounts through its ExtraRoutes slot (the OpenAI-compatible API on a loopback listener and the
// org endpoint receiver) bring their own authentication, and neither reads a v2 token. This mounts the real ones, as
// the daemon does, and presents genuine v2 secrets to each.

// routeCalls is every route the two mounts register, with a request for it. mountedRoutes must equal its length.
var routeCalls = []struct{ method, path string }{
	{"GET", "/v1/models"},
	{"GET", "/v1/models/some-model"},
	{"POST", "/v1/chat/completions"},
	{"POST", "/v1/images/generations"},
	{"POST", "/org-endpoint/{endpoint}"},
}

// mountedRoutes counts the mux.Handle / mux.HandleFunc calls of the two registrars, read from their source, so a route
// added to either fails the test until it is listed in routeCalls.
func mountedRoutes(t *testing.T) int {
	t.Helper()
	n := 0
	for _, f := range []string{"../../openaiapi/register.go", "../../orgbridge/receiver.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(nd ast.Node) bool {
			call, ok := nd.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Handle" || sel.Sel.Name == "HandleFunc") {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "mux" {
					n++
				}
			}
			return true
		})
	}
	return n
}

func TestNoExtraRouteAcceptsAV2Token(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := testdb.Open(t).DB
	ctx := context.Background()
	rt, err := newAPIRuntime(db, Flags{}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	mount := rt.MainMount("127.0.0.1:9322")
	if mount == nil {
		t.Skip("this process does not serve /v1")
	}

	// an org endpoint that has a credential file, so its bearer check is the one that runs
	ep, err := orggrant.NewStore(db).CreateEndpoint(ctx, "default", "growth", "bot", "wf")
	if err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(cred, []byte("endpoint-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE org_endpoints SET credential_file = ? WHERE id = ?`, cred, ep.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	composeRoutes((&orgbridge.Receiver{DB: db}).Register, mount)(mux)

	// genuine v2 secrets: a mission token and a session token, rows, bindings and a host start all valid
	key, err := procbind.RowKey(ctx, db, "default")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := procbind.InsertHost(ctx, db, key, "default", "gen-1", now); err != nil {
		t.Fatal(err)
	}
	var secrets []string
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
		if err := procbind.MintTx(ctx, db, key, procbind.MintRequest{Profile: "default", WorkerPID: os.Getpid(), WorkerStamp: "s", HostGeneration: "gen-1", Now: now, Tokens: []procbind.MintToken{tok}}); err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, secret)
	}

	if got := mountedRoutes(t); got != len(routeCalls) {
		t.Fatalf("the two registrars mount %d routes, routeCalls lists %d: a route was added or removed; list it and it is presented a v2 token", got, len(routeCalls))
	}
	for _, rc := range routeCalls {
		path := strings.ReplaceAll(rc.path, "{endpoint}", ep.ID)
		for _, secret := range secrets {
			req := httptest.NewRequest(rc.method, path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+secret)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s with a v2 token (%s...): status %d, want 401; %s", rc.method, rc.path, secret[:5], rec.Code, rec.Body)
			}
		}
	}
}
