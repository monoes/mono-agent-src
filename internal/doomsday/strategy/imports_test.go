package strategy_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The strategy libraries get session ids and text, nothing that carries authority: no vault, no token or approval
// material, no finish internals, no CLI, no coder host. They are reachable only through the tagged wiring, so they also
// start nothing at import.
func TestStrategyPackagesImportNothingThatCarriesAuthority(t *testing.T) {
	forbidden := []string{
		"/internal/secrets", "/internal/vault", "/internal/agenttokens", "/internal/coderhost", "/internal/cli",
		"/internal/doomsday/finish", "/internal/doomsday/stop", "/internal/doomsday/store", "/internal/mcp",
	}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			t.Errorf("%s: %v", p, perr)
			return nil
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if strings.Contains(path, bad) {
					t.Errorf("%s imports %s", p, path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A default build links none of the strategy layer but its plain types (which the ops package's operation handlers name):
// every other file that imports a strategy package is behind the doomsday build tag.
func TestOnlyTaggedFilesImportTheStrategyLibraries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var bad []string
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/doomsday/strategy/") {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly|parser.ParseComments)
			if perr != nil {
				return nil
			}
			tagged := false
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if strings.HasPrefix(c.Text, "//go:build") && strings.Contains(c.Text, "doomsday") && !strings.Contains(c.Text, "!doomsday") {
						tagged = true
					}
				}
				break
			}
			for _, im := range f.Imports {
				path, _ := strconv.Unquote(im.Path.Value)
				if strings.Contains(path, "internal/doomsday/strategy/") && !strings.HasSuffix(path, "/strategy/types") && !tagged {
					bad = append(bad, rel+" imports "+path)
				}
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("untagged files import the strategy libraries:\n%s", strings.Join(bad, "\n"))
	}
}
