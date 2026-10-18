package mcp

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The eval scorer (scripts/evals/evallib/scorer.py) keeps its own copy of the
// global tools, MCP_GLOBAL_TOOLS, to tell a tool call that must name a
// profile from one that takes none. It must say what globalToolsExpected says.
func TestEvalScorerGlobalToolsMatch(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "evals", "evallib", "scorer.py"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)MCP_GLOBAL_TOOLS\s*=\s*frozenset\(\[(.*?)\]\)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("scorer.py has no MCP_GLOBAL_TOOLS = frozenset([...])")
	}
	var python []string
	for _, n := range regexp.MustCompile(`"([^"]+)"`).FindAllSubmatch(m[1], -1) {
		python = append(python, string(n[1]))
	}
	slices.Sort(python)
	want := slices.Sorted(slices.Values(globalToolsExpected))
	if !slices.Equal(python, want) {
		t.Errorf("scorer.py MCP_GLOBAL_TOOLS = %s\nglobalToolsExpected     = %s", strings.Join(python, ","), strings.Join(want, ","))
	}
}
