package strategy_test

import (
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

// The strategy libraries get session ids and text, nothing that carries authority: no vault, no token or approval
// material, no finish internals, no CLI, no coder host. They are reachable only through the wiring, so they also
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

// Outside its own tree the strategy libraries (everything but the plain types, which the ops package's operation handlers
// name) are imported by exactly the wiring's files.
func TestOnlyTheWiringFilesImportTheStrategyLibraries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var got []string
	visited := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		werr := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err // an unreadable directory must not hide an importer
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			visited++
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/doomsday/strategy/") {
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
				path, _ := strconv.Unquote(im.Path.Value)
				if strings.Contains(path, "internal/doomsday/strategy/") && !strings.HasSuffix(path, "/strategy/types") {
					got = append(got, rel)
					break
				}
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", top, werr)
		}
	}
	if visited < 500 {
		t.Fatalf("only %d Go files visited: the walk is not looking at the tree", visited)
	}
	sort.Strings(got)
	want := []string{
		"internal/cli/doomsday/strategyengine.go", "internal/cli/doomsday/strategyreader.go", "internal/cli/doomsday/strategyready.go",
		"internal/cli/doomsday/strategysink.go", "internal/cli/doomsday/strategywire.go",
		// The mission pursuit loop (T6, S4) runs the same epoch engine over a mission's own store: the host's loop, off unless
		// the person started a pursuit. Its own import guard is coderhost/pursuit_guard_test.go.
		"internal/coderhost/pursuit.go", "internal/coderhost/pursuit_facts.go", "internal/coderhost/pursuit_wake.go",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("importers of the strategy libraries changed:\n got %v\nwant %v\ndecide the new importer here, in review", got, want)
	}
}
