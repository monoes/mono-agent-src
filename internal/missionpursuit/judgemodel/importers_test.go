package judgemodel

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// importersOf lists the non-test files outside dir that import the package path suffix, and how many files were visited.
func importersOf(t *testing.T, suffix, skipDir string) ([]string, int) {
	t.Helper()
	root := filepath.Join("..", "..", "..")
	var got []string
	visited := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			visited++
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, skipDir) {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, suffix) {
					got = append(got, rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	sort.Strings(got)
	return got, visited
}

// S5a builds the judge model but wires it nowhere: Host.Pursuit stays nil in production. The slice that wires it lists its
// file here, in review.
func TestNothingInTheProductTreeImportsTheJudgeModelYet(t *testing.T) {
	got, visited := importersOf(t, "/internal/missionpursuit/judgemodel", "internal/missionpursuit/judgemodel/")
	if visited < 500 {
		t.Fatalf("visited %d files: the scan is not looking at the tree", visited)
	}
	if len(got) != 0 {
		t.Fatalf("importers of the judge model: %v: decide the wiring in review (the pursuit loop is off in production)", got)
	}
}

// The cost context helper is the only seam between the judge model and the host's meter: two files use it.
func TestOnlyTheAdapterAndTheMeterUseTheCostContext(t *testing.T) {
	got, visited := importersOf(t, "/internal/missionpursuit/judgecost", "internal/missionpursuit/judgecost/")
	want := "internal/coderhost/pursuit_meter.go,internal/missionpursuit/judgemodel/judgemodel.go"
	if visited < 500 || strings.Join(got, ",") != want {
		t.Fatalf("importers %v (visited %d), want %s", got, visited, want)
	}
}
