package scorer

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The scorer is pure: it imports the strategy types and the standard library without process, network, database or file
// access, and nothing that carries authority or reaches the vault, the coder host, the CLI or the other strategy libraries.
func TestScorerImportsOnlyPlainPackages(t *testing.T) {
	allowedModule := map[string]bool{"github.com/monoes/mono-agent/internal/doomsday/strategy/types": true}
	forbiddenStd := []string{"os", "os/exec", "os/signal", "net", "database/sql", "syscall", "unsafe", "io/ioutil", "plugin", "runtime/debug"}
	n := 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		n++
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("%s: %v", p, perr)
			return nil
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
				if !allowedModule[path] {
					t.Errorf("%s imports %s", p, path)
				}
				continue
			}
			for _, bad := range forbiddenStd {
				if path == bad || bad == "net" && strings.HasPrefix(path, "net/") {
					t.Errorf("%s imports %s", p, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("only %d source files seen", n)
	}
}

// Nothing imports the scorer yet; the wiring (S4) is decided here, in review.
func TestNothingImportsTheScorerYet(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	seen := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			seen++
			rel, _ := filepath.Rel(root, p)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/missionpursuit/scorer/") {
				return nil
			}
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, "/internal/missionpursuit/scorer") {
					t.Errorf("%s imports the scorer: decide the new importer here, in review", rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	if seen < 500 {
		t.Fatalf("only %d Go files visited: the walk is not looking at the tree", seen)
	}
}
