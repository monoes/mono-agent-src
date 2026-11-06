package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// wails v2.17.0 hands a dialog's message to gtk_message_dialog_new as its printf FORMAT with no arguments, so a '%' in a
// name, a path, an error or a charter garbles it or reads memory. On Linux every '%' is doubled; macOS and Windows show
// text as it is, and doubling there would show "%%".
func TestDialogTextDoublesPercentWhereGTKReadsAFormat(t *testing.T) {
	for _, in := range []string{"%s%n%x", "100%", "%", "%%", "90% of files", "ends with %", "no percent", "%d %s %p", ""} {
		got := dialogTextFor("linux", in)
		if strings.Count(got, "%") != 2*strings.Count(in, "%") {
			t.Errorf("%q -> %q", in, got)
		}
		// what GTK will do with it: format it with no arguments. It must come out as the original text.
		if shown := fmt.Sprintf(got, []any{}...); shown != in {
			t.Errorf("%q is shown as %q", in, shown)
		}
		for _, goos := range []string{"windows", "darwin"} {
			if dialogTextFor(goos, in) != in {
				t.Errorf("%s changed %q", goos, in)
			}
		}
	}
	for _, goos := range []string{"freebsd", "openbsd", "netbsd"} { // GTK too
		if dialogTextFor(goos, "5%") != "5%%" {
			t.Errorf("%s does not double", goos)
		}
	}
}

// Every Wails dialog call of this module puts its title and message through dialogText (or gives a literal): a new call
// with dynamic text that skips it fails here. (A Dependabot bump of wails must be checked against this behaviour.)
func TestEveryMessageDialogUsesDialogText(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	calls := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "MessageDialog" {
				return true
			}
			calls++
			for _, arg := range call.Args[1:] {
				lit, ok := arg.(*ast.CompositeLit)
				if !ok {
					t.Errorf("%s: build the dialog options inline so the guard can see their text", fset.Position(arg.Pos()))
					continue
				}
				for _, el := range lit.Elts {
					kv, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, _ := kv.Key.(*ast.Ident)
					if key == nil || (key.Name != "Title" && key.Name != "Message") {
						continue
					}
					switch v := kv.Value.(type) {
					case *ast.BasicLit: // a literal has no dynamic text
					case *ast.CallExpr:
						if id, _ := v.Fun.(*ast.Ident); id == nil || id.Name != "dialogText" {
							t.Errorf("%s: a dialog %s is not built with dialogText", fset.Position(kv.Pos()), key.Name)
						}
					default:
						t.Errorf("%s: a dialog %s is not built with dialogText", fset.Position(kv.Pos()), key.Name)
					}
				}
			}
			return true
		})
	}
	if calls < 4 {
		t.Fatalf("found only %d MessageDialog calls: the guard is not seeing them", calls)
	}
}
