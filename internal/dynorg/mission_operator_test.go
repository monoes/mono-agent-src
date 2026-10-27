package dynorg

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

// operatorSites are the functions that may act as the operator (operatorActor), by
// "file: function". Each is a write the store reserves for the operator. Adding one needs a
// reason here, in review.
var operatorSites = map[string]string{
	"mission_close.go: proposeClose":             "attaches the final report to the integration task (report artifacts of a done task are the operator's); the session is never closed here",
	"mission_exec.go: recordSpend":               "AddSessionSpend: only this process records a worker's cost",
	"mission_integration.go: requeueIntegration": "an integration check that had begun goes back to Ready when the tree changed under it (Move out of Review or Done is the operator's)",
	"mission_merges.go: recordBranch":            "the store reserves branch artifacts for the operator's process",
	"mission_merges.go: recordMerge":             "the store reserves merge artifacts for the operator's process",
}

// No code of this package builds a Human actor or calls operatorActor outside operatorSites:
// a worker-facing path that acts as the operator could pass its own work or close a session.
func TestOperatorActorOnlyAtItsAllowlistedSites(t *testing.T) {
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := name + ": " + fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if id, ok := x.X.(*ast.Ident); ok && id.Name == "tasks" && x.Sel.Name == "Human" && name != "mission_operator.go" {
						t.Errorf("%s: tasks.Human outside mission_operator.go; use operatorActor at an allowlisted site", key)
					}
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "operatorActor" {
						seen[key] = true
					}
				}
				return true
			})
		}
	}
	var got []string
	for k := range seen {
		got = append(got, k)
		if _, ok := operatorSites[k]; !ok {
			t.Errorf("%s acts as the operator and is not in operatorSites", k)
		}
	}
	sort.Strings(got)
	for k := range operatorSites {
		if !seen[k] {
			t.Errorf("operatorSites lists %s, which no longer acts as the operator: remove it", k)
		}
	}
}
