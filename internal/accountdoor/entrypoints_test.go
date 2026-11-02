package accountdoor

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// doors is the inventory of every place the daemon accepts a connection or
// registers a route (account gate B3b, Task 9, issue #368), with what guards it.
// A new site fails TestEveryEntryPointIsAccountedFor until it is added here, which
// is the moment to ask whether a locked account can reach work through it.
var doors = map[string]string{
	"internal/account/oauth.go":           "the sign-in redirect catcher: open by design, it is how a login lands",
	"internal/cli/apiruntime/runtime.go":  "dedicated /v1 listener: Gateway.Serve, every route behind Gateway.auth",
	"internal/cli/daemon/daemon.go":       "HTTP API listener: httpapi.Server.Serve behind accountGate",
	"internal/cli/httpapicli/httpapi.go":  "HTTP API listener: httpapi.Server.ListenAndServe behind accountGate",
	"internal/connections/oauth.go":       "a connection's OAuth redirect catcher, started by a gated command or by the app; stores a token, starts no work",
	"internal/connections/oauth1_flow.go": "a connection's OAuth 1.0a redirect catcher, started by a gated command or by the app; stores a token, starts no work",
	"internal/extension/cdp.go":           "/monoagent/cdp upgrade: token, then account (upgrade and each command)",
	"internal/extension/server.go":        "bridge: /monoagent/* routes and the extension socket; see TestTheOpenEndpointsOfALockedBridge",
	"internal/extension/server_auth.go":   "bridge: the extension socket's upgrade (handleWS, split out of server.go), token first; see TestTheOpenEndpointsOfALockedBridge",
	"internal/httpapi/server.go":          "HTTP API routes: auth wrapper plus accountGate",
	"internal/openaiapi/register.go":      "/v1 routes, each behind Gateway.auth",
	"internal/openaiapi/serve.go":         "dedicated /v1 listener and its /health",
	"internal/procbind/sockdir_linux.go":  "the binding service's unix socket: a 0700 directory of the user, peers of that uid only, and it answers whether a caller may use a bound token; it runs no workflow, calls no gated command and uses no account, so a locked account gains nothing through it; nothing starts it yet",
	"internal/orgbridge/receiver.go":      "POST /org-endpoint/{id}: Receiver.ServeHTTP gate first",
	"internal/workflow/webhook_server.go": "webhook listener: WebhookServer.ServeHTTP gate first",
}

func TestEveryEntryPointIsAccountedFor(t *testing.T) {
	root := filepath.Join("..", "..")
	skipDir := map[string]bool{"wails-app": true, ".git": true, ".claude": true, "node_modules": true, "chrome-extension": true, "docs": true}
	found := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return nil // a file this build does not compile is no door
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			recv, _ := sel.X.(*ast.Ident)
			switch sel.Sel.Name {
			case "ListenAndServe", "ListenAndServeTLS", "ListenUnix", "ListenPacket", "Upgrade":
			case "Listen":
				if recv == nil || recv.Name != "net" {
					return true
				}
			case "Handle", "HandleFunc":
				if recv == nil || (recv.Name != "mux" && recv.Name != "http") {
					return true
				}
			default:
				return true
			}
			rel, _ := filepath.Rel(root, path)
			found[filepath.ToSlash(rel)] = true
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var unknown, stale []string
	for file := range found {
		if _, ok := doors[file]; !ok {
			unknown = append(unknown, file)
		}
	}
	for file := range doors {
		if !found[file] {
			stale = append(stale, file)
		}
	}
	sort.Strings(unknown)
	sort.Strings(stale)
	if len(unknown) > 0 {
		t.Errorf("these files accept connections or register routes and are not in the doors inventory: %v\nDecide what stops a locked account at each, add its test, then list it.", unknown)
	}
	if len(stale) > 0 {
		t.Errorf("the doors inventory lists files that no longer have an entry point: %v", stale)
	}
}
