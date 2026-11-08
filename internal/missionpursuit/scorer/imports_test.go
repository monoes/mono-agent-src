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

// The scorer is pure: it imports the standard library and the strategy types, nothing that carries authority or reaches the
// database, the vault, the coder host, the CLI or the other strategy libraries.
func TestScorerImportsOnlyPlainPackages(t *testing.T) {
	allowedModule := map[string]bool{"github.com/monoes/mono-agent/internal/doomsday/strategy/types": true}
	n := 0
	_ = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
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
			if strings.Contains(strings.SplitN(path, "/", 2)[0], ".") && !allowedModule[path] {
				t.Errorf("%s imports %s", p, path)
			}
		}
		return nil
	})
	if n == 0 {
		t.Fatal("no source files seen")
	}
}

// Nothing imports the scorer yet; the wiring (S4) is decided here, in review.
func TestNothingImportsTheScorerYet(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if strings.HasPrefix(filepath.ToSlash(rel), "internal/missionpursuit/scorer/") {
				return nil
			}
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
			if perr != nil {
				return nil
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, "/internal/missionpursuit/scorer") {
					t.Errorf("%s imports the scorer: decide the new importer here, in review", rel)
				}
			}
			return nil
		})
	}
}
