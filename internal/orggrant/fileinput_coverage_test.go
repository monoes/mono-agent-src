package orggrant

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var nodeTypeRe = regexp.MustCompile(`Type\(\) string\s*\{\s*return "([^"]+)"`)

// Every node type declared in a file that calls fsconfine must be known to
// the "files from input" warning, or `org grant add` stays silent about a
// node that reads or writes a path the role's arguments can choose.
func TestEveryFsconfineNodeIsListedForTheFileInputWarning(t *testing.T) {
	nodesDir := filepath.Join("..", "nodes")
	var missing []string
	err := filepath.WalkDir(nodesDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "fsconfine.") {
			return nil
		}
		for _, m := range nodeTypeRe.FindAllStringSubmatch(string(src), -1) {
			_, file := fileNodeTypes[m[1]]
			_, image := imageNodeTypes[m[1]]
			if !file && !image {
				missing = append(missing, m[1]+" ("+p+")")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("node types that call fsconfine but are not in fileNodeTypes/imageNodeTypes: %s", strings.Join(missing, ", "))
	}
}
