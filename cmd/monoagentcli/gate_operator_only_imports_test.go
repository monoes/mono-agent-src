package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	pathpkg "path"
	"strconv"
	"testing"
)

// countMarkerOnlyChecks counts, in one Go source, the marker-only operator checks that T1 (the guard
// of gate_operator_only_guard_test.go) allow-lists: operator.Refusal, Marker, MarkerEnv and Caller through the
// package internal/operator under whatever local name it is imported (a renamed or a dot import
// included), and the identifiers notOperator and IsAgentContext.
// It reads the syntax tree, so comments and strings never count and an import alias cannot hide a call.
func countMarkerOnlyChecks(t testing.TB, name string, src []byte) int {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	// package path -> the selectors that are marker-only checks
	watched := map[string]map[string]bool{
		"github.com/monoes/mono-agent/internal/operator": {"Refusal": true, "Marker": true, "MarkerEnv": true, "Caller": true},
	}
	local := map[string]map[string]bool{} // local package name -> watched selectors
	dot := map[string]bool{}              // selectors reachable unqualified through a dot import
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		sels := watched[path]
		if sels == nil {
			continue
		}
		switch {
		case imp.Name == nil:
			local[pathpkg.Base(path)] = sels
		case imp.Name.Name == ".":
			for s := range sels {
				dot[s] = true
			}
		case imp.Name.Name != "_":
			local[imp.Name.Name] = sels
		}
	}
	n := 0
	qualified := map[*ast.Ident]bool{} // the Sel of every selector expression
	ast.Inspect(f, func(node ast.Node) bool {
		if sel, ok := node.(*ast.SelectorExpr); ok {
			qualified[sel.Sel] = true
			if x, ok := sel.X.(*ast.Ident); ok && local[x.Name][sel.Sel.Name] {
				n++
			}
		}
		return true
	})
	ast.Inspect(f, func(node ast.Node) bool {
		id, ok := node.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case id.Name == "notOperator" || id.Name == "IsAgentContext":
			n++
		case dot[id.Name] && !qualified[id]:
			n++
		}
		return true
	})
	return n
}

func TestMarkerOnlyChecksAreFoundWhateverTheImportIsCalled(t *testing.T) {
	const imp = `"github.com/monoes/mono-agent/internal/operator"`
	cases := []struct {
		name, src string
		want      int
	}{
		{"plain", "package p\nimport " + imp + "\nvar _ = operator.Refusal()\n", 1},
		{"alias", "package p\nimport op " + imp + "\nvar _ = op.Refusal()\nvar _ = op.Marker\n", 2},
		{"dot import", "package p\nimport . " + imp + "\nvar _ = Refusal()\n", 1},
		{"MarkerEnv and Caller", "package p\nimport o " + imp + "\nvar _ = o.MarkerEnv()\nvar _ = o.Caller\n", 2},
		{"blank import", "package p\nimport _ " + imp + "\n", 0},
		{"comments and strings", "package p\nimport " + imp + "\n// operator.Refusal()\nvar _ = \"operator.Refusal\"\n", 0},
		{"another package called operator", "package p\nimport \"example.com/operator\"\nvar _ = operator.Refusal()\n", 0},
		{"notOperator and IsAgentContext", "package p\nvar _ = notOperator()\nvar _ = x.IsAgentContext()\n", 2},
	}
	for _, c := range cases {
		if got := countMarkerOnlyChecks(t, c.name+".go", []byte(c.src)); got != c.want {
			t.Errorf("%s: counted %d, want %d", c.name, got, c.want)
		}
	}
}
