package credhttp_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// directClients are the non-test files that build an http.Client (a
// literal, new(http.Client), a variable or a struct field of the value
// type), use http.DefaultClient, or call http.Get/Head/Post/PostForm
// themselves, with how many such uses each has and why it needs no
// credhttp: it carries no credential, or it has a redirect policy of its
// own at least as strict for what it sends. A client passed straight to
// credhttp.Client doesn't count. A request that carries a credential goes
// through credhttp.Client (or credhttp.CheckRedirect with credhttp.Mark).
//
// Not covered: the clients third-party SDKs build inside themselves.
// slack-go (comm.slack), telegram-bot-api (comm.telegram) and gofeed
// (system.rss_read) send to their fixed service hosts with clients this
// scan can't see.
var directClients = map[string]struct {
	n   int
	why string
}{
	"cmd/monoagentcli/release_e2e_hook.go":              {1, "the release end-to-end hook's loopback transport; no credential"},
	"internal/account/refresh.go":                       {1, "the monoes.me refresh follows no redirect (ErrUseLastResponse)"},
	"internal/agentinstall/agentinstall.go":             {1, "downloads public installers; no credential"},
	"internal/apiconfig/probe.go":                       {1, "the provider probe follows no redirect (ErrUseLastResponse)"},
	"internal/apipkg/execute.go":                        {1, "the packages' base client: every request goes out through opRun.client() or checkedClient(), which set credhttp.CheckRedirect"},
	"internal/apipkg/execute_output.go":                 {1, "the TLS-skipping base client of the same, under the same policy"},
	"internal/appupdate/appupdate.go":                   {1, "public release downloads; no credential"},
	"internal/automation/install.go":                    {1, "public automation downloads; its redirects stay on https"},
	"internal/cli/doctor/env_services.go":               {1, "the local daemon's /health on this machine; no credential"},
	"internal/cli/integrations/integration.go":          {1, "fetches a public OpenAPI document; no credential"},
	"internal/cli/update/app.go":                        {1, "public release downloads; no credential"},
	"internal/cli/update/update.go":                     {2, "public GitHub release metadata and downloads; no credential"},
	"internal/crawlsite/crawl.go":                       {1, "crawls public pages; its checkRedirect keeps to the crawl's hosts and public addresses"},
	"internal/credhttp/credhttp.go":                     {1, "credhttp.Client itself: the policy every credentialed client gets"},
	"internal/discovery/robots.go":                      {1, "reads a public robots.txt; no credential"},
	"internal/discovery/sources/arbeitnow/arbeitnow.go": {1, "a public job feed; no credential"},
	"internal/discovery/sources/jobicy/jobicy.go":       {1, "a public job feed; no credential"},
	"internal/discovery/sources/linkedin/robots.go":     {1, "reads a public robots.txt; no credential"},
	"internal/extension/proof.go":                       {1, "bridge is a loopback, token-proof-verified peer, no redirects: newBridgeClient"},
	"internal/nodemgr/nodemgr.go":                       {1, "downloads Node.js releases; no credential"},
	"internal/nodes/ai/crawl/engine.go":                 {1, "reads public pages; its CheckRedirect guards every hop's address"},
	"internal/nodes/http/send.go":                       {1, "http.request's own redirectPolicy: drops the request's credentials on another host unless send_credentials_on_redirect"},
	"internal/nodes/image/remove_background_cgo.go":     {1, "downloads a public model; no credential"},
	"internal/personphoto/personphoto.go":               {1, "public profile photos; its CheckRedirect guards every hop"},
	"internal/release/client.go":                        {1, "signed release downloads; its CheckRedirect keeps to the release hosts"},
	"internal/workflow/webhook_handshake.go":            {1, "confirms an SNS subscription at an allowed AWS host; no credential"},
	"scripts/bench/vs-n8n/loadgen/main.go":              {1, "a local benchmark load generator; no credential"},
}

// Every direct HTTP client in the non-test code is one directClients
// lists with its reason; a new one fails until it goes through credhttp or
// is listed.
func TestNoUnreviewedDirectHTTPClient(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string]int{}
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", "vendor", ".git", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if n := directUses(f); n > 0 {
			got[filepath.ToSlash(rel)] = n
		}
		if n := discardedMarks(f); n > 0 {
			t.Errorf("%s drops the request credhttp.Mark returns (%d): use it, or the redirect policy never sees the mark", filepath.ToSlash(rel), n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for f := range got {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		want, ok := directClients[f]
		switch {
		case !ok:
			t.Errorf("%s builds or uses an HTTP client directly (%d): send credentialed requests through credhttp.Client, or list it in directClients with why it needs none", f, got[f])
		case want.n != got[f]:
			t.Errorf("%s has %d direct HTTP client uses, directClients says %d: review the new one", f, got[f], want.n)
		}
	}
	for f := range directClients {
		if _, ok := got[f]; !ok {
			t.Errorf("directClients lists %s, which has no direct HTTP client now", f)
		}
	}
}

// directUses counts f's http.Client literals and new(http.Client) calls
// (not those passed straight to credhttp.Client), its http.DefaultClient
// uses and its http.Get/Head/Post/PostForm calls.
func directUses(f *ast.File) int {
	httpName := ""
	for _, imp := range f.Imports {
		if imp.Path.Value == `"net/http"` {
			httpName = "http"
			if imp.Name != nil {
				httpName = imp.Name.Name
			}
		}
	}
	if httpName == "" {
		return 0
	}
	isHTTP := func(e ast.Expr, names ...string) bool {
		sel, ok := e.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != httpName {
			return false
		}
		for _, n := range names {
			if sel.Sel.Name == n {
				return true
			}
		}
		return false
	}
	wrapped := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Client" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "credhttp" {
				for _, a := range call.Args {
					if u, ok := a.(*ast.UnaryExpr); ok {
						a = u.X
					}
					wrapped[a] = true
				}
			}
		}
		return true
	})
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.CompositeLit:
			if isHTTP(x.Type, "Client") && !wrapped[x] {
				n++
			}
		case *ast.CallExpr:
			if isHTTP(x.Fun, "Get", "Head", "Post", "PostForm") {
				n++
			}
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 && isHTTP(x.Args[0], "Client") {
				n++
			}
		case *ast.SelectorExpr:
			if isHTTP(x, "DefaultClient") {
				n++
			}
		case *ast.ValueSpec:
			if x.Type != nil && isHTTP(x.Type, "Client") {
				n += len(x.Names) // var c http.Client
			}
		case *ast.Field:
			if isHTTP(x.Type, "Client") {
				n++ // a struct field of type http.Client
			}
		}
		return true
	})
	return n
}

// discardedMarks counts the credhttp.Mark calls whose result f drops:
// Mark returns the marked request, so a dropped one marks nothing.
func discardedMarks(f *ast.File) int {
	isMark := func(e ast.Expr) bool {
		call, ok := e.(*ast.CallExpr)
		if !ok {
			return false
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Mark" {
			return false
		}
		id, ok := sel.X.(*ast.Ident)
		return ok && id.Name == "credhttp"
	}
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.ExprStmt:
			if isMark(x.X) {
				n++
			}
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				if id, ok := x.Lhs[i].(*ast.Ident); ok && id.Name == "_" && isMark(rhs) {
					n++
				}
			}
		}
		return true
	})
	return n
}

// The scan sees every form of a direct client, and a dropped Mark.
func TestDirectUsesSeesEveryForm(t *testing.T) {
	src := `package p

import (
	"net/http"

	"x/credhttp"
)

type holder struct {
	c   http.Client
	ok  *http.Client
}

var a http.Client
var b = &http.Client{}
var c = credhttp.Client(&http.Client{})
var d = new(http.Client)

func f(r *http.Request) {
	_, _ = http.DefaultClient.Do(r)
	_, _ = http.Get("u")
	credhttp.Mark(r)
	_ = credhttp.Mark(r, "X")
	r = credhttp.Mark(r)
}
`
	f, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	// holder.c, a, b, d, DefaultClient, Get: c is wrapped, ok is a pointer.
	if n := directUses(f); n != 6 {
		t.Errorf("directUses = %d, want 6", n)
	}
	if n := discardedMarks(f); n != 2 {
		t.Errorf("discardedMarks = %d, want 2", n)
	}
}
