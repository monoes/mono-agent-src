package verifier

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// sources are the non-test Go files of this package. Walk errors are returned, and an empty walk fails: a guard that looks at
// nothing passes on anything.
func sources(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p != "." {
			return filepath.SkipDir
		}
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 8 {
		t.Fatalf("only %d source files: the guard is not looking at the package", len(out))
	}
	return out
}

// The library runs programs and asks a model, and nothing else: no tasks store, no host, no CLI, no vault or secrets, no MCP,
// no storage, no direct network, no other mission-pursuit package that holds authority.
func TestThePackageImportsNothingThatCarriesAuthority(t *testing.T) {
	forbidden := []string{"net/http", "net", "plugin", "unsafe", "database/sql",
		"/internal/storage", "/internal/secrets", "/internal/vault", "/internal/agenttokens", "/internal/coderhost", "/internal/cli",
		"/internal/mcp", "/internal/tasks", "/internal/dynorg", "/internal/doomsday/store", "/internal/doomsday/stop",
		"/internal/doomsday/strategy", "/internal/missionpursuit/judgemodel", "/internal/missionpursuit/inputs",
		"/internal/missionpursuit/strategy", "/internal/missionpursuit/scorer", "/internal/account", "/wails-app", "/cmd/"}
	for _, p := range sources(t) {
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if path == bad || (strings.HasPrefix(bad, "/") && strings.Contains(path, bad)) {
					t.Errorf("%s imports %s", p, path)
				}
			}
			if path == "github.com/monoes/mono-agent/internal/missionpursuit" {
				t.Errorf("%s imports the pursuit store package", p)
			}
		}
	}
}

// Only the store's host-checks file imports the library (it maps a Result to rows, S7b); the host step (a later slice) will be
// the other importer, decided in review.
func TestNothingImportsThePackageYet(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var got []string
	visited := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		werr := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			visited++
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/missionpursuit/verifier/") {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, "/internal/missionpursuit/verifier") {
					got = append(got, rel)
				}
			}
			return nil
		})
		if werr != nil {
			t.Fatalf("walking %s: %v", top, werr)
		}
	}
	if visited < 500 {
		t.Fatalf("the walk visited %d Go files: the guard would pass on nothing", visited)
	}
	sort.Strings(got)
	if want := []string{"internal/missionpursuit/checks_host.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("importers of the goal verifier library: %v, want %v\ndecide the importer here, in review", got, want)
	}
}

// No file declares an init(): the finish sandbox's limits helper is run from the command's run function, and nothing in this
// package may do I/O before main.
func TestThePackageDeclaresNoInit(t *testing.T) {
	for _, p := range sources(t) {
		if strings.Contains(mustRead(t, p), "\nfunc init()") {
			t.Errorf("%s declares init()", p)
		}
	}
}
