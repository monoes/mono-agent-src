package testhome

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNoTestSetsHomeAlone fails for a test file anywhere in the repository
// (the wails-app module included) that calls Setenv("HOME", …) directly. On
// Windows the account's home is USERPROFILE (and APPDATA, LOCALAPPDATA), so
// a test that moves HOME alone keeps working on the real profile there and
// its code under test reads and writes the real ~/.monoagent. Use SetHome.
func TestNoTestSetsHomeAlone(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Setenv" {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && s == "HOME" {
					rel, _ := filepath.Rel(root, path)
					offenders = append(offenders, filepath.ToSlash(rel)+":"+strconv.Itoa(fset.Position(call.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 500 {
		t.Fatalf("scanned only %d test files under %s: is the walk broken?", files, root)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("%d test call(s) set HOME alone; use testhome.SetHome(t, dir) (it sets USERPROFILE, APPDATA, LOCALAPPDATA too on Windows):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func TestHomeOnlyEnv(t *testing.T) {
	get := func(env [][2]string) map[string]string {
		m := map[string]string{}
		for _, kv := range env {
			m[kv[0]] = kv[1]
		}
		return m
	}
	t.Run("unix sets HOME and USERPROFILE", func(t *testing.T) {
		m := get(homeOnlyEnv("linux", "/h/x"))
		if len(m) != 2 || m["HOME"] != "/h/x" || m["USERPROFILE"] != "/h/x" {
			t.Errorf("got %v", m)
		}
	})
	t.Run("windows redirects every profile variable", func(t *testing.T) {
		m := get(homeOnlyEnv("windows", `C:\s\h`))
		want := map[string]string{
			"HOME": `C:\s\h`, "USERPROFILE": `C:\s\h`, "HOMEDRIVE": "C:", "HOMEPATH": `\s\h`,
			"APPDATA": `C:\s\h\AppData\Roaming`, "LOCALAPPDATA": `C:\s\h\AppData\Local`,
		}
		for k, v := range want {
			if m[k] != v {
				t.Errorf("%s = %q, want %q", k, m[k], v)
			}
		}
	})
}

func TestSetHomeMovesBothVariables(t *testing.T) {
	dir := t.TempDir()
	SetHome(t, dir)
	for _, k := range []string{"HOME", "USERPROFILE"} {
		if got := os.Getenv(k); got != dir {
			t.Errorf("%s = %q, want %q", k, got, dir)
		}
	}
}
