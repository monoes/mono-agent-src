package jevconf

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const (
	jevPath     = "github.com/monoes/mono-agent/internal/jev"
	jevconfPath = "github.com/monoes/mono-agent/internal/jev/jevconf"
)

// keyOnlyChecks may call ResolveKey without building a client: they only
// look whether a key resolves.
var keyOnlyChecks = map[string]string{
	"internal/cli/org/autonomy_decider.go": "reports whether a key resolves, sends nothing",
	"internal/cli/jevcli/jev.go":           "`jev enable` reports whether a key resolves, sends nothing",
}

// bypasses lists the ways the source of one non-test file builds a TypeSafe
// client itself, reads its host, or resolves a key apart from jevconf's
// ResolveClient. It resolves imports (so an aliased import counts) and reads
// the syntax, not text: a comment or a string that says "jev.NewClient(" is not
// a use, `j.NewClient`, a method value, `new(jev.Client)`, `&jev.Client{}`,
// `var c jev.Client` and a struct or parameter of the type by value are.
func bypasses(file string, src []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return []string{file + ": does not parse: " + err.Error()}
	}
	var out []string
	add := func(n ast.Node, what string) {
		out = append(out, file+":"+strconv.Itoa(fset.Position(n.Pos()).Line)+": "+what)
	}
	jevNames, confNames := map[string]bool{}, map[string]bool{}
	for _, im := range f.Imports {
		p, _ := strconv.Unquote(im.Path.Value)
		name := ""
		if im.Name != nil {
			name = im.Name.Name
		}
		switch p {
		case jevPath:
			if name == "." {
				add(im, "dot-imports the jev package: its constructor and Client are then unreachable to this check")
			}
			if name == "" {
				name = "jev"
			}
			jevNames[name] = true
		case jevconfPath:
			if name == "" {
				name = "jevconf"
			}
			confNames[name] = true
		}
	}
	isJev := func(e ast.Expr, sel string) bool {
		s, ok := e.(*ast.SelectorExpr)
		if !ok || s.Sel.Name != sel {
			return false
		}
		id, ok := s.X.(*ast.Ident)
		return ok && jevNames[id.Name]
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if isJev(x, "NewClient") {
				add(x, "uses jev.NewClient (a call, a method value, through any import alias)")
			}
			if id, ok := x.X.(*ast.Ident); ok && confNames[id.Name] && x.Sel.Name == "ResolveKey" {
				if _, ok := keyOnlyChecks[file]; !ok {
					add(x, "uses jevconf.ResolveKey: build the client with jevconf.ResolveClient")
				}
			}
		case *ast.CompositeLit:
			if isJev(x.Type, "Client") {
				add(x, "builds a jev.Client by composite literal")
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "new" && len(x.Args) == 1 && isJev(x.Args[0], "Client") {
				add(x, "allocates a jev.Client with new")
			}
		case *ast.ValueSpec:
			if x.Type != nil && isJev(x.Type, "Client") {
				add(x, "declares a jev.Client value")
			}
		case *ast.Field:
			if isJev(x.Type, "Client") {
				add(x, "holds a jev.Client by value (use *jev.Client)")
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING && (x.Value == `"TYPESAFE_BASE_URL"` || x.Value == "`TYPESAFE_BASE_URL`") {
				add(x, "names TYPESAFE_BASE_URL: only jevconf.BaseURL decides the host")
			}
		}
		return true
	})
	return out
}

// Every shape of a bypass is caught, and legitimate code is not.
func TestThePinSeesEveryShapeOfBypass(t *testing.T) {
	const head = "package p\nimport (\n\t\"os\"\n\tj \"github.com/monoes/mono-agent/internal/jev\"\n\t\"github.com/monoes/mono-agent/internal/jev/jevconf\"\n)\nvar _ = os.Getenv\nvar _ = jevconf.NewClient\n"
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"aliased constructor call", "func f() { j.NewClient(\"k\", \"\") }", 1},
		{"method value", "func f() { g := j.NewClient; _ = g }", 1},
		{"new", "func f() { _ = new(j.Client) }", 1},
		{"address of a literal", "func f() { _ = &j.Client{APIKey: \"k\", BaseURL: \"http://x\"} }", 1},
		{"bare literal", "func f() { _ = j.Client{} }", 1},
		{"var of the value type", "func f() { var c j.Client; _ = c }", 1},
		{"package var", "var c j.Client", 1},
		{"struct field by value", "type s struct{ c j.Client }", 1},
		{"parameter by value", "func f(c j.Client) {}", 1},
		{"env host read", "func f() string { return os.Getenv(\"TYPESAFE_BASE_URL\") }", 1},
		{"env host read through a constant", "const n = \"TYPESAFE_BASE_URL\"", 1},
		{"ResolveKey", "func f() { jevconf.ResolveKey(nil, nil, \"\", \"\") }", 1},
		{"ok: a client by pointer", "func f(c *j.Client) *j.Client { return c }", 0},
		{"ok: jevconf's own constructor", "func f() { jevconf.NewClient(nil, nil, \"\", \"\", \"\", \"\") }", 0},
		{"ok: a comment and a string that mention it", "// j.NewClient( and TYPESAFE_API_KEY\nfunc f() string { return \"j.NewClient(\" }", 0},
	} {
		if got := bypasses("x.go", []byte(head+c.body)); len(got) != c.want {
			t.Errorf("%s: %d finding(s) %v, want %d", c.name, len(got), got, c.want)
		}
	}
	if got := bypasses("x.go", []byte("package p\nimport . \"github.com/monoes/mono-agent/internal/jev\"\nvar _ = NewClient")); len(got) != 1 {
		t.Errorf("dot import: %v", got)
	}
	if got := bypasses("internal/cli/org/autonomy_decider.go", []byte(head+"func f() { jevconf.ResolveKey(nil, nil, \"\", \"\") }")); len(got) != 0 {
		t.Errorf("an allowlisted key-only check: %v", got)
	}
}

// Every way the TypeSafe key leaves the process goes through this package:
// ResolveKey reads it (the grant check and audit apply to the implicit lookup
// as to an explicit @secret: reference) and ResolveClient pairs it with its
// host (BaseURL). A new caller that builds its own client or reads
// TYPESAFE_BASE_URL itself would bypass both; this pin fails for it.
func TestEveryUserOfTheTypeSafeKeyGoesThroughJevconf(t *testing.T) {
	_, here, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(here), "..", "..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if n := d.Name(); n != "." && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasPrefix(rel, "internal/jev/") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		for _, f := range bypasses(rel, b) {
			t.Errorf("%s\n\tbuild the client with jevconf.NewClient or jevconf.ResolveClient, so the key goes only where jevconf.BaseURL says and its read is gated", f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// The read of a stored key is gated: the lookup refuses an unapproved step
	// and goes through secrets.Resolve under agent access (readEntry).
	b, err := os.ReadFile(filepath.Join(filepath.Dir(here), "key.go"))
	if err != nil {
		t.Fatal(err)
	}
	key := string(b)
	if !strings.Contains(key, "secrets.RefusedReference(ctx, secretRef(c.name))") || !strings.Contains(key, "readEntry(ctx, db, c.scope, c.name)") {
		t.Error("ResolveKey no longer refuses and audits the read of the stored key before using it")
	}
	if n := strings.Count(key, "entryValue("); n != 3 { // its definition, readEntry, Adopt
		t.Errorf("entryValue( appears %d times in key.go, want 3 (definition, readEntry, Adopt): a new caller reads the vault key without the gate", n)
	}

	// vector.store's embedding block sends its provider key only to the host
	// its connection or the user's workflow names.
	vec, err := os.ReadFile(filepath.Join(root, "internal/nodes/vector/credentials.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(vec), "workflow.CheckDestinationKeys(") {
		t.Error("internal/nodes/vector/credentials.go no longer checks where a connection's key goes")
	}
}
