package missionpursuit

import (
	"go/ast"
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

// The host verifier's capability (HostChecks) writes a check and its machine results. Its safety is that nothing but the host
// constructs it: no CLI verb, no MCP tool, no agent path. This guard lists every construction site in internal/, cmd/ and
// wails-app/. A new site fails the test and must be decided in review: add it here with the reason.
var knownHostChecksSites = map[string]string{
	// none yet: the host step (internal/coderhost/pursuit_check.go) is the first, in the slice that wires the verifier
}

// allowedHostChecksDirs are where a construction site may be at all.
var allowedHostChecksDirs = []string{"internal/coderhost/"}

func scanHostChecksSites(t *testing.T, base string) (sites []string, visited int) {
	t.Helper()
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		err := filepath.WalkDir(filepath.Join(base, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(base, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/missionpursuit/") && !strings.Contains(strings.TrimPrefix(rel, "internal/missionpursuit/"), "/") {
				return nil // the package's own files define it
			}
			f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if perr != nil {
				return perr
			}
			visited++
			sel := map[*ast.Ident]bool{}
			ast.Inspect(f, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					sel[x.Sel] = true
					if x.Sel.Name == "NewHostChecks" || x.Sel.Name == "HostChecks" {
						sites = append(sites, rel+":"+x.Sel.Name)
					}
				case *ast.Ident:
					// a bare name: a dot import, or a copy of the name in another package that wraps it
					if x.Name == "NewHostChecks" && !sel[x] {
						sites = append(sites, rel+":NewHostChecks")
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	sort.Strings(sites)
	return sites, visited
}

func TestOnlyTheHostConstructsTheCheckCapability(t *testing.T) {
	sites, visited := scanHostChecksSites(t, filepath.Join("..", ".."))
	if visited < 500 {
		t.Fatalf("the walk visited %d Go files: the guard would pass on nothing", visited)
	}
	for _, s := range sites {
		file := s[:strings.Index(s, ":")]
		ok := false
		for _, d := range allowedHostChecksDirs {
			ok = ok || strings.HasPrefix(file, d)
		}
		if !ok {
			t.Errorf("%s reaches the host check capability from outside internal/coderhost", s)
			continue
		}
		if _, known := knownHostChecksSites[file]; !known {
			t.Errorf("%s is a new site of the host check capability: decide it in review and list it here", s)
		}
	}
}

func TestTheGuardSeesAConstructionSite(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"internal/cli/task", "cmd/x", "wails-app"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := "package task\nimport mp \"github.com/monoes/mono-agent/internal/missionpursuit\"\nfunc f(s *mp.Store) { _ = mp.NewHostChecks(s); var _ mp.HostChecks }\n"
	if err := os.WriteFile(filepath.Join(dir, "internal/cli/task/x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, visited := scanHostChecksSites(t, dir)
	if visited != 1 || len(sites) != 2 {
		t.Fatalf("visited %d, sites %v: the scan is blind", visited, sites)
	}
}

// directImports of the package's own non-test files.
func directImports(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			out[e.Name()] = append(out[e.Name()], p)
		}
	}
	if len(out) < 8 {
		t.Fatalf("only %d source files: the guard is not looking at the package", len(out))
	}
	return out
}

// The store reads and writes rows. It starts no program and opens no connection of its own: the verifier library is the one
// that runs programs, and the host is the one that calls it.
func TestTheStoreStartsNoProgramAndOpensNoConnection(t *testing.T) {
	for file, imps := range directImports(t) {
		for _, p := range imps {
			switch p {
			case "os/exec", "net", "net/http", "syscall", "plugin", "unsafe":
				t.Errorf("%s imports %s", file, p)
			}
		}
	}
}

// Only checks_host.go imports the verifier library: the store's other files neither need it nor may grow to depend on it.
func TestOnlyTheHostFileImportsTheVerifier(t *testing.T) {
	for file, imps := range directImports(t) {
		for _, p := range imps {
			if strings.HasSuffix(p, "/internal/missionpursuit/verifier") && file != "checks_host.go" {
				t.Errorf("%s imports the verifier library", file)
			}
		}
	}
}

// The host verifier is not the person: its file writes no task_session_events row, names no actor, and never says "you".
func TestTheHostFileNeverSpeaksForThePerson(t *testing.T) {
	raw, err := os.ReadFile("checks_host.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, bad := range []string{"task_session_events", "tasks.Actor", "tasks.Human", "Label()", "mission_pursuit SET", "UPDATE mission_pursuit", `"you"`, "mission_goal_answers"} {
		for _, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, bad) {
				t.Errorf("checks_host.go contains %q: %s", bad, strings.TrimSpace(line))
			}
		}
	}
}
