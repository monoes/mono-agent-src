package all

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// forbiddenHTTP are the net/http identifiers an adapter must not use: each
// builds or uses a client other than jev.KeyedHTTPClient, which reads no
// proxy settings and drops the key on a redirect to another host.
var forbiddenHTTP = map[string]string{
	"Client":        "builds its own http.Client",
	"DefaultClient": "uses http.DefaultClient",
	"Get":           "uses http.Get (the default client)",
	"Post":          "uses http.Post (the default client)",
	"Head":          "uses http.Head (the default client)",
	"PostForm":      "uses http.PostForm (the default client)",
}

// httpBypasses lists the places file's source builds or uses an HTTP client
// of its own. The import is resolved, so an aliased net/http counts; a
// comment or string does not. A *http.Client in a type or a field is fine:
// only a composite literal, new(http.Client) and the package-level helpers
// are uses.
func httpBypasses(file string, src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return []string{file + ": does not parse: " + err.Error()}
	}
	var out []string
	add := func(n ast.Node, what string) {
		out = append(out, file+":"+strconv.Itoa(fset.Position(n.Pos()).Line)+": "+what)
	}
	names := map[string]bool{}
	for _, im := range f.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == "net/http" {
			name := "http"
			if im.Name != nil {
				name = im.Name.Name
			}
			if name == "." {
				add(im, "dot-imports net/http: its client is then unreachable to this check")
			}
			names[name] = true
		}
	}
	isHTTP := func(e ast.Expr) (string, bool) {
		s, ok := e.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		id, ok := s.X.(*ast.Ident)
		return s.Sel.Name, ok && names[id.Name]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if sel, ok := isHTTP(n.Type); ok && sel == "Client" {
				add(n, forbiddenHTTP["Client"])
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "new" && len(n.Args) == 1 {
				if sel, ok := isHTTP(n.Args[0]); ok && sel == "Client" {
					add(n, forbiddenHTTP["Client"])
				}
			}
		case *ast.SelectorExpr:
			if sel, ok := isHTTP(n); ok && sel != "Client" {
				if what, bad := forbiddenHTTP[sel]; bad {
					add(n, what)
				}
			}
		}
		return true
	})
	return out
}

// No adapter under internal/jev/providers builds its own HTTP client or
// uses the default one: every request carrying a key goes through
// jev.KeyedHTTPClient.
func TestAdaptersUseOnlyTheKeyedHTTPClient(t *testing.T) {
	root := ".."
	seen := map[string]bool{}
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		seen[filepath.Base(filepath.Dir(path))] = true
		found = append(found, httpBypasses(path, src)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []string{"openaicompat", "anthropic", "hfzeroshot", "decisionsapi"} {
		if !seen[pkg] {
			t.Fatalf("the walk saw no source of %s (saw %v): the check would pass without checking", pkg, seen)
		}
	}
	for _, f := range found {
		t.Error(f)
	}
}

// The check finds every form it forbids, and nothing in a comment, a string
// or a type.
func TestTheHTTPClientCheckFindsEachForm(t *testing.T) {
	src := `package x

import (
	h "net/http"
	"time"
)

// http.Client{} in a comment is no use.
var s = "http.DefaultClient"

type holder struct{ c *h.Client }

func f(c *h.Client) {
	_ = &h.Client{Timeout: time.Second}
	_ = h.Client{}
	_ = new(h.Client)
	_ = h.DefaultClient
	_, _ = h.Get("u")
	_, _ = h.Post("u", "", nil)
	_, _ = h.Head("u")
	_, _ = h.PostForm("u", nil)
	_, _ = h.NewRequest("GET", "u", nil)
}
`
	got := httpBypasses("x.go", []byte(src))
	if len(got) != 8 {
		t.Fatalf("%d findings, want 8:\n%s", len(got), strings.Join(got, "\n"))
	}
}
