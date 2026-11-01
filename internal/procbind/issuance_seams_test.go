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
	if bad := scanIssuanceSeams(t, filepath.Join("..", "..")); len(bad) > 0 {
		t.Errorf("non-test code installs a verifier or moves the issuance switch (or could not be checked):\n  %s", strings.Join(bad, "\n  "))
	}
}

// scanIssuanceSeams lists the non-test Go files under root that reach SetV2Verifier or ForTestSetIssuance, and those it
// cannot parse (a file that cannot be read is a failure, not a pass). Any selector on the name counts, called or not and
// whatever the import alias is; so does a bare call, which is what a dot import or an in-package call looks like. The
// wails-app module is scanned too: it can import the internal packages through its replace.
func scanIssuanceSeams(t *testing.T, root string) []string {
	t.Helper()
	guarded := map[string]bool{"SetV2Verifier": true, "ForTestSetIssuance": true}
	var bad []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "chrome-extension", "scripts", "docs":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			bad = append(bad, path+": unparsable: "+perr.Error())
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if guarded[x.Sel.Name] {
					bad = append(bad, path+": "+x.Sel.Name)
				}
			case *ast.CallExpr:
				if id, ok := x.Fun.(*ast.Ident); ok && guarded[id.Name] {
					bad = append(bad, path+": "+id.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return bad
}

// Each gap of the scan stays closed: every fixture below must be reported, and clean code must not be.
func TestTheIssuanceSeamScanCatchesWhatItShould(t *testing.T) {
	for name, c := range map[string]struct {
		dir, file, src string
		want           bool
	}{
		"direct call":       {"x", "a.go", "package x\nimport \"p\"\nfunc f() { p.SetV2Verifier(nil) }\n", true},
		"aliased import":    {"x", "a.go", "package x\nimport pb \"p\"\nfunc f() { pb.ForTestSetIssuance(true) }\n", true},
		"dot import":        {"x", "a.go", "package x\nimport . \"p\"\nfunc f() { SetV2Verifier(nil) }\n", true},
		"method value":      {"x", "a.go", "package x\nimport \"p\"\nvar f = p.ForTestSetIssuance\n", true},
		"wails-app module":  {"wails-app", "a.go", "package main\nimport \"p\"\nfunc f() { p.SetV2Verifier(nil) }\n", true},
		"unparsable file":   {"x", "a.go", "package x\nfunc (", true},
		"test file is fine": {"x", "a_test.go", "package x\nimport \"p\"\nfunc f() { p.SetV2Verifier(nil) }\n", false},
		"clean code":        {"x", "a.go", "package x\nfunc f() {}\n", false},
	} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, c.dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, c.dir, c.file), []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := len(scanIssuanceSeams(t, root)) > 0; got != c.want {
			t.Errorf("%s: reported = %v, want %v", name, got, c.want)
		}
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
