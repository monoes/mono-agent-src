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

// The host verifier's capability (HostChecks) writes a check and its machine results; the person's writers (AnswerRubric,
// SetUnconfinedChecks) and the operator view of a check (LatestCheck, GetCheck) are the person's. Their safety is WHO CALLS THEM:
// the person's writers take a tasks.Actor{Kind: Human}, which is only a struct, and other code (dynorg, coderhost) already builds
// such an actor for host writes. So every call site in the repository is listed by directory. A new site fails the test and must
// be decided in review.
type sitePolicy struct {
	names   []string // selectors (or, for the constructor, bare identifiers too) that mean the thing
	allowed []string // directories (prefixes) where a use may be
	defined []string // the files that define it (their declarations are not uses)
}

var sitePolicies = []sitePolicy{
	{names: []string{"NewHostChecks", "HostChecks"}, allowed: []string{"internal/coderhost/"}, defined: []string{"internal/missionpursuit/checks_host.go"}},
	{names: []string{"AnswerRubric", "SetUnconfinedChecks", "LatestCheck", "GetCheck"}, allowed: []string{"internal/cli/task/"}, defined: []string{"internal/missionpursuit/checks_answer.go", "internal/missionpursuit/checks.go"}},
}

// knownHostChecksSites lists the files that construct the host capability: the host's check step and its start-up sweep.
var knownHostChecksSites = map[string]string{
	"internal/coderhost/pursuit_check.go":       "runs a check and records it",
	"internal/coderhost/pursuit_check_sweep.go": "ends the checks an earlier host left running",
}

// scanSites walks the whole repository under base (Go files, tests excluded; scripts, tools and tests directories included;
// only .git and dependency folders are skipped), returns "file:Name" per use and how many files it parsed.
func scanSites(t *testing.T, base string) (sites []string, visited int) {
	t.Helper()
	watch := map[string]bool{}
	for _, p := range sitePolicies {
		for _, n := range p.names {
			watch[n] = true
		}
	}
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Only the repository's own .git and its root-level dependency folders are skipped: a folder that merely has such a
			// name deeper in the tree (internal/zz/vendor) is code like any other.
			if r, _ := filepath.Rel(base, path); r == ".git" || r == "vendor" || r == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(base, path)
		rel = filepath.ToSlash(rel)
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
				if watch[x.Sel.Name] {
					sites = append(sites, rel+":"+x.Sel.Name)
				}
			case *ast.BasicLit:
				// the names as strings (reflect.MethodByName, a method table) reach the same methods
				if x.Kind == token.STRING {
					if v, err := strconv.Unquote(x.Value); err == nil && watch[v] {
						sites = append(sites, rel+":"+v)
					}
				}
			case *ast.Ident:
				// a bare name: a dot import, or the package's own code calling the constructor
				if x.Name == "NewHostChecks" && !sel[x] {
					sites = append(sites, rel+":NewHostChecks")
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", base, err)
	}
	sort.Strings(sites)
	return sites, visited
}

func TestOnlyTheRightCodeReachesTheCheckWriters(t *testing.T) {
	sites, visited := scanSites(t, filepath.Join("..", ".."))
	if visited < 500 {
		t.Fatalf("the walk visited %d Go files: the guard would pass on nothing", visited)
	}
	for _, s := range sites {
		file, name := s[:strings.Index(s, ":")], s[strings.Index(s, ":")+1:]
		for _, p := range sitePolicies {
			for _, n := range p.names {
				if n != name {
					continue
				}
				ok := false
				for _, d := range p.allowed {
					ok = ok || strings.HasPrefix(file, d)
				}
				for _, d := range p.defined {
					ok = ok || file == d
				}
				if !ok {
					t.Errorf("%s: %s is reached from outside %v: decide it in review and list it here", file, name, p.allowed)
				}
			}
		}
		if name == "NewHostChecks" && strings.HasPrefix(file, "internal/coderhost/") {
			if _, known := knownHostChecksSites[file]; !known {
				t.Errorf("%s is a new site of the host check capability: decide it in review and list it here", file)
			}
		}
	}
}

func TestTheGuardSeesAConstructionSite(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"internal/cli/chat", "scripts/tools", "cmd/x", "wails-app"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := "package x\nimport mp \"github.com/monoes/mono-agent/internal/missionpursuit\"\nfunc f(s *mp.Store) { _ = mp.NewHostChecks(s); var _ mp.HostChecks; s.AnswerRubric(); s.SetUnconfinedChecks() }\n"
	for _, f := range []string{"internal/cli/chat/x.go", "scripts/tools/y.go"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal/zz/vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := "package v\nimport mp \"github.com/monoes/mono-agent/internal/missionpursuit\"\nfunc f(s *mp.Store) { _ = mp.NewHostChecks(s) }\n"
	if err := os.WriteFile(filepath.Join(dir, "internal/zz/vendor/x.go"), []byte(deep), 0o644); err != nil {
		t.Fatal(err)
	}
	refl := "package r\nimport \"reflect\"\nfunc g(v any) { reflect.ValueOf(v).MethodByName(\"SetUnconfinedChecks\") }\n"
	if err := os.WriteFile(filepath.Join(dir, "scripts/tools/r.go"), []byte(refl), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, visited := scanSites(t, dir)
	// the vendor folder at the root is skipped, the one deeper is not; a name as a string is a site
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vendor/v.go"), []byte(deep), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, _ := scanSites(t, dir); len(again) != len(sites) {
		t.Fatalf("the root vendor folder was scanned: %v then %v", sites, again)
	}
	if visited != 4 || len(sites) != 10 {
		t.Fatalf("visited %d, sites %v: the scan is blind (it must see scripts and tools too)", visited, sites)
	}
}

// Nothing but NewHostChecks (and the methods of HostChecks itself) may take or return the capability: a method that hands it out
// would give it to every caller of the Store.
func TestNothingElseHandsOutTheCheckCapability(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	mentions := func(e ast.Expr) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "HostChecks" {
				found = true
			}
			return !found
		})
		return found
	}
	seen := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		seen++
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || !fd.Name.IsExported() || fd.Name.Name == "NewHostChecks" {
				continue
			}
			if fd.Recv != nil && len(fd.Recv.List) == 1 && mentions(fd.Recv.List[0].Type) {
				continue // a method of HostChecks itself
			}
			if mentions(fd.Type) {
				t.Errorf("%s: %s takes or returns the host check capability", e.Name(), fd.Name.Name)
			}
		}
	}
	if seen < 8 {
		t.Fatalf("only %d source files", seen)
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
