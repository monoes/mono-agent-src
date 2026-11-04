package stop

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// MACs are compared in constant time: a witness verdict is visible to a local caller (the control room's badge), so a
// byte-wise compare would be an oracle. No non-test file of the witness may call bytes.Equal.
func TestWitnessFilesCompareMACsInConstantTime(t *testing.T) {
	files, _ := filepath.Glob("witness*.go")
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "Equal" {
					if id, ok := s.X.(*ast.Ident); ok && id.Name == "bytes" {
						t.Errorf("%s: bytes.Equal is not constant time", fset.Position(c.Pos()))
					}
				}
			}
			return true
		})
	}
}
