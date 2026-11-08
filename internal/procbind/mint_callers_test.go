package procbind

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

// mintTxAllowed are the production files that may reference MintTx. Each must end every token of a request by its own id on any error
// and validate the request (profile, scope, expiry) first; see the contract on MintTx.
var mintTxAllowed = []string{"cli/chat/coder_host_bound.go"}

// mintTxFiles lists, relative to root, the non-test Go files outside the procbind package that reference MintTx (any selector under an
// import alias, a bare name through a dot import, or the function as a value).
func mintTxFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			t.Errorf("%s: unparsable: %v", path, perr)
			return nil
		}
		if f.Name.Name == "procbind" {
			return nil // the definition's own package
		}
		pb, dot := procbindNames(f), dotImportsProcbind(f)
		found := false
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if isPkgCall(x, pb, "MintTx") {
					found = true
				}
			case *ast.Ident:
				if dot && x.Name == "MintTx" {
					found = true
				}
			}
			return true
		})
		if found {
			rel, _ := filepath.Rel(root, path)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func TestMintTxHasOnlyTheLauncherAsAProductionCaller(t *testing.T) {
	got := mintTxFiles(t, filepath.Join("..", ".."))
	var rel []string
	for _, g := range got {
		rel = append(rel, strings.TrimPrefix(g, "internal/"))
	}
	if strings.Join(rel, ",") != strings.Join(mintTxAllowed, ",") {
		t.Errorf("MintTx is referenced from %v, want only %v: a new caller must keep MintTx's contract (end every token by its own id on any error; validate profile, scope and expiry) and be added to mintTxAllowed on purpose", rel, mintTxAllowed)
	}
}

func TestTheMintTxScanFindsEveryWayToReferenceIt(t *testing.T) {
	const imp = "\"github.com/monoes/mono-agent/internal/procbind\""
	for name, c := range map[string]struct {
		src  string
		want bool
	}{
		"qualified":      {"package x\nimport " + imp + "\nfunc f() { procbind.MintTx(ctx, db, k, r) }\n", true},
		"aliased":        {"package x\nimport pb " + imp + "\nfunc f() { pb.MintTx(ctx, db, k, r) }\n", true},
		"dot import":     {"package x\nimport . " + imp + "\nfunc f() { MintTx(ctx, db, k, r) }\n", true},
		"function value": {"package x\nimport " + imp + "\nvar m = procbind.MintTx\n", true},
		"elsewhere":      {"package x\nimport \"example.com/other\"\nfunc f() { other.MintTx() }\n", false},
		"bare, no dot":   {"package x\nimport " + imp + "\nfunc f() { MintTx() }\n", false},
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(c.src), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := len(mintTxFiles(t, root)) > 0; got != c.want {
			t.Errorf("%s: reported = %v, want %v", name, got, c.want)
		}
	}
}
