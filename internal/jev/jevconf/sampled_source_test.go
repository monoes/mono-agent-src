package jevconf

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// These tests read the repository's source, so that no surface gets the
// sampled lowering (sampled.go) without being pinned here: not a new one, and
// not an old one borrowing a listed surface's name.

const repoRoot = "../../.."

// sampledUses is every use, outside sampled.go, of what can lower a
// threshold: the gate functions (with the surface they name) and the
// adapters' unanimous probability.
var sampledUses = []string{
	"internal/captureclassify/classify.go: GateThreshold(jevconf.Capture)",
	"internal/cli/jevcli/retry.go: GateThreshold(jevconf.Retry)",
	"internal/cli/people/messages_classify.go: GateThreshold(jevconf.Inbox)",
	"internal/openaiapi/auto.go: CapThreshold",
	"internal/openaiapi/auto_jev.go: SampledCeiling(jevconf.APIAuto)",
	"internal/peoplelinks/suggest.go: GateThreshold(jevconf.PeopleLinks)",
	"internal/jev/jevtest/sampled.go: Unanimous",
	"internal/jev/jevtest/sampled.go: UnanimousProbability",
	"internal/jev/providers/anthropic/anthropic.go: Unanimous",
	"internal/jev/providers/anthropic/anthropic.go: UnanimousProbability",
	"internal/jev/providers/openaicompat/backend.go: Unanimous",
	"internal/jev/providers/openaicompat/turn.go: UnanimousProbability",
}

// nodeSurfaceUses is every jevconf.NodeSurface call: a new decision node
// must be added to surfaceDecisions.
var nodeSurfaceUses = []string{
	`internal/matching/evaluate_jev.go: "applications.evaluate"`,
	`internal/nodes/ai/choose/choose.go: NodeType`,
	`internal/nodes/ai/decide/decide.go: NodeType`,
	`internal/nodes/browserjev/node.go: "browser.jev"`,
	`internal/nodes/decisionclient/decisionclient.go: "ai.choose"`,
	`internal/nodes/decisionclient/decisionclient.go: "ai.decide"`,
}

// goSources calls f with every non-test Go file of the repository whose text
// holds one of words.
func goSources(t *testing.T, words []string, f func(rel string, file *ast.File)) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Fatalf("no repository at %s: %v", repoRoot, err)
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path != repoRoot && (strings.HasPrefix(name, ".") || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(words, func(w string) bool { return bytes.Contains(src, []byte(w)) }) {
			return nil
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		f(filepath.ToSlash(rel), file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// importName is the name file uses for the package at path ("" = not imported).
func importName(file *ast.File, path string) string {
	for _, im := range file.Imports {
		if p, _ := strconv.Unquote(im.Path.Value); p == path {
			if im.Name != nil {
				return im.Name.Name
			}
			return path[strings.LastIndex(path, "/")+1:]
		}
	}
	return ""
}

func assertUses(t *testing.T, got, want []string, advice string) {
	t.Helper()
	slices.Sort(got)
	got = slices.Compact(got)
	want = slices.Sorted(slices.Values(want))
	if !slices.Equal(got, want) {
		t.Errorf("%s\ngot:\n  %s\nwant:\n  %s", advice, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestOnlyPinnedCodeCanLowerASampledThreshold(t *testing.T) {
	gates := []string{"GateThreshold", "SampledCeiling", "CapThreshold"}
	var got []string
	goSources(t, append([]string{"Unanimous"}, gates...), func(rel string, file *ast.File) {
		if rel == "internal/jev/jevconf/sampled.go" {
			return
		}
		conf := importName(file, "github.com/monoes/mono-agent/internal/jev/jevconf")
		seen := map[*ast.SelectorExpr]bool{} // a call, already recorded with its surface
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok || conf == "" || types.ExprString(sel.X) != conf || !slices.Contains(gates, sel.Sel.Name) {
					return true
				}
				use := rel + ": " + sel.Sel.Name
				if sel.Sel.Name != "CapThreshold" && len(n.Args) > 0 {
					use += "(" + types.ExprString(n.Args[0]) + ")"
				}
				got, seen[sel] = append(got, use), true
			case *ast.SelectorExpr:
				name := n.Sel.Name
				if name == "Unanimous" || name == "UnanimousProbability" ||
					(!seen[n] && conf != "" && types.ExprString(n.X) == conf && slices.Contains(gates, name)) {
					got = append(got, rel+": "+name)
				}
			case *ast.KeyValueExpr:
				if k, ok := n.Key.(*ast.Ident); ok && k.Name == "Unanimous" {
					got = append(got, rel+": Unanimous")
				}
			case *ast.Ident:
				// Inside jevconf the gates are named without a package.
				if file.Name.Name == "jevconf" && slices.Contains(gates, n.Name) {
					got = append(got, rel+": "+n.Name)
				}
			}
			return true
		})
	})
	assertUses(t, got, sampledUses,
		"a sampled threshold is lowered for low-risk surfaces only: pin a new use here, and its surface in surfaceDecisions")
}

func TestEverySurfaceHasADecision(t *testing.T) {
	var nodes []string
	goSources(t, []string{"NodeSurface", "Surface = "}, func(rel string, file *ast.File) {
		conf := importName(file, "github.com/monoes/mono-agent/internal/jev/jevconf")
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && conf != "" && types.ExprString(sel.X) == conf &&
					sel.Sel.Name == "NodeSurface" && len(n.Args) == 1 {
					nodes = append(nodes, rel+": "+types.ExprString(n.Args[0]))
				}
			case *ast.ValueSpec:
				if file.Name.Name != "jevconf" || n.Type == nil || types.ExprString(n.Type) != "Surface" {
					return true
				}
				for i, v := range n.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok {
						t.Errorf("%s: surface %s is not a string literal", rel, n.Names[i].Name)
						continue
					}
					s, _ := strconv.Unquote(lit.Value)
					if _, ok := surfaceDecisions[Surface(s)]; !ok {
						t.Errorf("surface %s (%q) has no entry in surfaceDecisions", n.Names[i].Name, s)
					}
				}
			}
			return true
		})
	})
	assertUses(t, nodes, nodeSurfaceUses,
		"a node's decision surface must be pinned here and in surfaceDecisions")
}
