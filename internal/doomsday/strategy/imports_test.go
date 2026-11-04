package strategy_test

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
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

// defaultTags are the tags a default build of each supported platform sets (no doomsday, no nosocial, no devaccount).
var defaultTags = map[string]map[string]bool{
	"linux":   {"linux": true, "unix": true, "amd64": true, "gc": true, "cgo": true},
	"darwin":  {"darwin": true, "unix": true, "arm64": true, "gc": true, "cgo": true},
	"windows": {"windows": true, "amd64": true, "gc": true},
}

// excludedFromDefaultBuild reports whether the file's //go:build constraint (read from EVERY comment group above the
// package clause, parsed as the go tool parses it) is false in a default build of every supported platform. A file with no
// constraint, or one that a default build satisfies on any platform (`doomsday || linux`, `!doomsday`), is compiled by
// default and so is not excluded.
func excludedFromDefaultBuild(src []byte) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false, err
	}
	var expr constraint.Expr
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			if expr, err = constraint.Parse(c.Text); err != nil {
				return false, err
			}
		}
	}
	if expr == nil {
		return false, nil
	}
	for _, tags := range defaultTags {
		tags := tags
		if expr.Eval(func(tag string) bool { return tags[tag] }) {
			return false, nil
		}
	}
	return true, nil
}

func TestTheBuildConstraintReaderIsExact(t *testing.T) {
	for _, c := range []struct {
		name, src string
		want      bool
	}{
		{"plain tag", "//go:build doomsday\n\npackage p\n", true},
		{"tag and platform", "//go:build doomsday && linux\n\npackage p\n", true},
		{"no constraint", "package p\n", false},
		{"or with a platform compiles by default", "//go:build doomsday || linux\n\npackage p\n", false},
		{"or with every platform", "//go:build doomsday || unix || windows\n\npackage p\n", false},
		{"negated", "//go:build !doomsday\n\npackage p\n", false},
		{"not the first comment group", "// Copyright\n// header\n\n//go:build doomsday\n\npackage p\n", true},
		{"after a doc comment block", "/* license */\n\n//go:build doomsday\n\npackage p\n", true},
		{"a mention in the package doc is not a constraint", "// uses go:build doomsday in prose\npackage p\n", false},
		{"a constraint below the package clause is not one", "package p\n\n//go:build doomsday\n", false},
	} {
		got, err := excludedFromDefaultBuild([]byte(c.src))
		if err != nil || got != c.want {
			t.Errorf("%s: got %v (%v), want %v", c.name, got, err, c.want)
		}
	}
}

// A default build links none of the strategy layer but its plain types (which the ops package's operation handlers name):
// every other file that imports a strategy package is excluded from a default build by its build constraint.
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
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, src, parser.ImportsOnly)
			if perr != nil {
				return nil
			}
			excluded, cerr := excludedFromDefaultBuild(src)
			if cerr != nil {
				t.Errorf("%s: %v", rel, cerr)
				return nil
			}
			for _, im := range f.Imports {
				path, _ := strconv.Unquote(im.Path.Value)
				if strings.Contains(path, "internal/doomsday/strategy/") && !strings.HasSuffix(path, "/strategy/types") && !excluded {
					bad = append(bad, rel+" imports "+path)
				}
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("files compiled by default import the strategy libraries:\n%s", strings.Join(bad, "\n"))
	}
}
