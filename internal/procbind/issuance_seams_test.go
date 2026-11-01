package procbind

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Nothing but a test can install a v2 verifier or move the issuance switch: SetV2Verifier and ForTestSetIssuance are
// called only from _test.go files (the stores install their verifier themselves, by procbind.IssuanceOn, in NewStore).
func TestOnlyTestsInstallAVerifierOrMoveTheIssuanceSwitch(t *testing.T) {
	guarded := map[string]bool{"SetV2Verifier": true, "ForTestSetIssuance": true}
	root := filepath.Join("..", "..")
	var bad []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "wails-app", "chrome-extension", "scripts", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && guarded[s.Sel.Name] {
					bad = append(bad, path+": "+s.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("non-test code installs a verifier or moves the issuance switch:\n  %s", strings.Join(bad, "\n  "))
	}
}

// With the constant false every v2 secret is denied by the stores (they hold no verifier); the seam, which a test uses
// to stand in for the flipped constant, turns it on and off and restores itself.
func TestTheIssuanceSwitchDefaultsOffAndTheSeamRestores(t *testing.T) {
	if IssuanceReady {
		t.Skip("issuance is on")
	}
	if IssuanceOn() {
		t.Fatal("issuance is on although the constant is false")
	}
	t.Run("seam", func(t *testing.T) {
		ForTestSetIssuance(t, true)
		if !IssuanceOn() {
			t.Error("the seam did not turn issuance on")
		}
	})
	if IssuanceOn() {
		t.Error("the seam did not restore the switch")
	}
}
