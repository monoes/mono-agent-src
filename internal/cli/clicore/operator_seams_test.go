package clicore

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// operatorSeams are the Deps fields a command asks whether its caller is the
// operator: RequireOperator, Operator (func(what) error), NotOperator and
// Refusal (func() string).
var operatorSeams = map[string]bool{"RequireOperator": true, "Operator": true, "NotOperator": true, "Refusal": true}

// No command calls its operator check itself: every call goes through
// RequireOperator or OperatorRefusal, which refuse when it isn't wired, so a
// new call site can't fail open (#864 review).
func TestNoCommandCallsItsOperatorCheckDirectly(t *testing.T) {
	fset := token.NewFileSet()
	dirs, err := os.ReadDir("..")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, dir := range dirs {
		if !dir.IsDir() || dir.Name() == "clicore" {
			continue
		}
		pkgs := parseDir(t, fset, filepath.Join("..", dir.Name()))
		for _, files := range pkgs {
			seams := depsSeams(files)
			if len(seams) == 0 {
				continue
			}
			checked++
			for _, file := range files {
				ast.Inspect(file, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && seams[sel.Sel.Name] {
						if pkgName, ok := sel.X.(*ast.Ident); !ok || (pkgName.Name != "clicore" && pkgName.Name != "operator") {
							t.Errorf("%s: calls %s itself; use clicore.RequireOperator or clicore.OperatorRefusal, which refuse when it isn't wired",
								fset.Position(call.Pos()), sel.Sel.Name)
						}
					}
					return true
				})
			}
		}
	}
	if checked < 6 {
		t.Errorf("found the operator checks of %d packages, want at least 6 (hil, chat, configcli, task, publication, apikey, ...)", checked)
	}
}

// depsSeams are the operator-check fields of pkg's Deps.
func depsSeams(files []*ast.File) map[string]bool {
	out := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Deps" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return false
			}
			for _, f := range st.Fields.List {
				if _, isFunc := f.Type.(*ast.FuncType); !isFunc {
					continue
				}
				for _, id := range f.Names {
					if operatorSeams[id.Name] {
						out[id.Name] = true
					}
				}
			}
			return false
		})
	}
	return out
}

// parseDir parses the non-test .go files of dir that this platform builds
// (go/build's constraint check), grouped by package name.
func parseDir(t *testing.T, fset *token.FileSet, dir string) map[string][]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if ok, err := build.Default.MatchFile(dir, name); err != nil || !ok {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name.Name] = append(out[f.Name.Name], f)
	}
	return out
}
