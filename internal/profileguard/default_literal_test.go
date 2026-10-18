package profileguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The regex rules see a fallback only when the variable is called profile or
// profileID. defaultLiteralHits reads the code instead: in a function that
// deals with profiles (any identifier, field or method in it says "profile"),
// the literal "default" -- or a constant whose value it is -- may not be
// assigned, returned, compared, passed or set as a field, whatever the
// variable is called. Outside functions, a field or variable whose name says
// profile may not be given it.
const defaultLiteralRule = "default-literal-in-profile-code"

type hit struct {
	rel  string
	line int
	rule string
	text string
}

// defaultLiteralHits parses every .go file of one directory (dir, shown as rel)
// and returns the uses of "default" in profile code.
func defaultLiteralHits(dir, rel string, files []string) ([]hit, error) {
	fset := token.NewFileSet()
	var parsed []*ast.File
	var names []string
	for _, f := range files {
		af, err := parser.ParseFile(fset, filepath.Join(dir, f), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, af)
		names = append(names, f)
	}
	// Constants of the package whose value is "default" stand for the literal.
	consts := map[string]bool{}
	for _, af := range parsed {
		ast.Inspect(af, func(n ast.Node) bool {
			if gd, ok := n.(*ast.GenDecl); ok && gd.Tok == token.CONST {
				for _, spec := range gd.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, v := range vs.Values {
						if isDefaultLit(v, nil) && i < len(vs.Names) {
							consts[vs.Names[i].Name] = true
						}
					}
				}
			}
			return true
		})
	}
	var hits []hit
	add := func(file string, n ast.Node, src []byte) {
		p := fset.Position(n.Pos())
		line := ""
		if lines := strings.Split(string(src), "\n"); p.Line-1 < len(lines) {
			line = strings.TrimSpace(lines[p.Line-1])
		}
		hits = append(hits, hit{rel: relFile(rel, file), line: p.Line, rule: defaultLiteralRule, text: line})
	}
	for i, af := range parsed {
		src, err := os.ReadFile(filepath.Join(dir, names[i]))
		if err != nil {
			return nil, err
		}
		for _, decl := range af.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body == nil || !mentionsProfile(d) {
					continue
				}
				for _, n := range defaultUses(d.Body, consts) {
					add(names[i], n, src)
				}
			case *ast.GenDecl:
				if d.Tok != token.VAR {
					// A const whose value is "default" is followed to its uses.
					continue
				}
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for j, v := range vs.Values {
						if j < len(vs.Names) && saysProfile(vs.Names[j].Name) && isDefaultLit(v, consts) {
							add(names[i], v, src)
						}
					}
				}
				ast.Inspect(d, func(n ast.Node) bool {
					if kv, ok := n.(*ast.KeyValueExpr); ok && isDefaultLit(kv.Value, consts) {
						if k, ok := kv.Key.(*ast.Ident); ok && saysProfile(k.Name) {
							add(names[i], kv, src)
						}
					}
					return true
				})
			}
		}
	}
	return hits, nil
}

func relFile(rel, file string) string {
	if rel == "." {
		return file
	}
	return rel + "/" + file
}

func saysProfile(name string) bool { return strings.Contains(strings.ToLower(name), "profile") }

// mentionsProfile reports whether any identifier of fn (its name, receiver,
// parameters, results or body) says profile.
func mentionsProfile(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && saysProfile(id.Name) {
			found = true
		}
		return !found
	})
	return found
}

// isDefaultLit reports whether e is the string literal "default" (either
// quoting) or a constant whose value it is.
func isDefaultLit(e ast.Expr, consts map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return false
		}
		s, err := strconv.Unquote(v.Value)
		return err == nil && s == "default"
	case *ast.Ident:
		return consts[v.Name]
	case *ast.ParenExpr:
		return isDefaultLit(v.X, consts)
	}
	return false
}

// defaultUses are the places in body where "default" is a value: assigned,
// declared, returned, compared, passed or set as a field (a switch case
// compares too).
func defaultUses(body *ast.BlockStmt, consts map[string]bool) []ast.Node {
	var out []ast.Node
	first := func(es []ast.Expr) ast.Expr {
		for _, e := range es {
			if isDefaultLit(e, consts) {
				return e
			}
		}
		return nil
	}
	ast.Inspect(body, func(n ast.Node) bool {
		var e ast.Expr
		switch v := n.(type) {
		case *ast.AssignStmt:
			e = first(v.Rhs)
		case *ast.ValueSpec:
			e = first(v.Values)
		case *ast.ReturnStmt:
			e = first(v.Results)
		case *ast.BinaryExpr:
			if v.Op == token.EQL || v.Op == token.NEQ {
				e = first([]ast.Expr{v.X, v.Y})
			}
		case *ast.CallExpr:
			e = first(v.Args)
		case *ast.KeyValueExpr:
			e = first([]ast.Expr{v.Value})
		case *ast.CaseClause:
			e = first(v.List)
		}
		if e != nil {
			out = append(out, e)
		}
		return true
	})
	return out
}

// scanDefaultLiterals runs defaultLiteralHits over every package directory
// under root, skipping what the walk of TestNoImplicitDefaultProfile skips.
func scanDefaultLiterals(t *testing.T, root string) []hit {
	t.Helper()
	byDir := map[string][]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], filepath.Base(p))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	var hits []hit
	for _, d := range dirs {
		rel, _ := filepath.Rel(root, d)
		h, err := defaultLiteralHits(d, filepath.ToSlash(rel), byDir[d])
		if err != nil {
			t.Fatal(err)
		}
		hits = append(hits, h...)
	}
	return hits
}

// Planted fallbacks the regexes cannot see -- a variable called id, a
// return, an argument, a field, a constant -- are caught; "default" in code
// that has nothing to do with profiles, and in comments, is not.
func TestDefaultLiteralRuleCatchesPlantedFallbacks(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("planted/fallback.go", `package planted

import "context"

func withProfileID(ctx context.Context, id string) context.Context { return ctx }

type Settings struct{ ProfileID string }

var fallbackSettings = Settings{ProfileID: "default"} // field

var defaultProfile = "default" // variable

const legacy = "default"

func resolveProfile(id string) string {
	if id == "" {
		id = "default" // the id variable the regexes miss
	}
	return id
}

func pickProfile(given string) string {
	if given != "" {
		return given
	}
	return "default" // a return
}

func run(ctx context.Context) context.Context {
	return withProfileID(ctx, "default") // an argument
}

func viaConst(profile string) string {
	id := profile
	if id == "" {
		id = legacy // a constant
	}
	return id
}

func isLegacyProfile(p string) bool {
	switch p {
	case "default": // a switch case
		return true
	}
	return false
}

func keepProfile(profileID string) bool { return profileID == "Default" } // another word
`)
	write("planted/unrelated.go", `package planted

// mode picks "default" here, but nothing in it is about profiles.
func mode(name string) string {
	if name == "" {
		return "default"
	}
	return name
}
`)
	var got []string
	for _, h := range scanDefaultLiterals(t, root) {
		got = append(got, h.rel+":"+h.text)
	}
	want := []string{
		`planted/fallback.go:var fallbackSettings = Settings{ProfileID: "default"} // field`,
		`planted/fallback.go:var defaultProfile = "default" // variable`,
		`planted/fallback.go:id = "default" // the id variable the regexes miss`,
		`planted/fallback.go:return "default" // a return`,
		`planted/fallback.go:return withProfileID(ctx, "default") // an argument`,
		`planted/fallback.go:id = legacy // a constant`,
		`planted/fallback.go:case "default": // a switch case`,
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("hits:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
