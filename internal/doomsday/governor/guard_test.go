package governor_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Guards of the open-ended mode's resource governor (note B3, section 6, acceptance A1 and A8): the mode is off and
// unreachable by default, and nothing an agent can reach writes a cap, a limit or the queue. Each guard states what it
// enumerates; a new importer, caller or exported setter fails a test until it is decided here, in review.

const (
	modRoot   = "github.com/monoes/mono-agent/"
	govPrefix = modRoot + "internal/doomsday/governor"
)

// repoRoot is the module root (this package is internal/doomsday/governor).
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("not the module root: %v", err)
	}
	return root
}

func goList(t *testing.T, root string, tags string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("no go tool to list dependencies: %v", err)
	}
	args := []string{"list", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, "-f", "{{.ImportPath}}", "./cmd/monoagentcli")
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go %v: %v\n%s", args, err, out)
	}
	return strings.Fields(string(out))
}

func governorPkgs(pkgs []string) []string {
	var out []string
	for _, p := range pkgs {
		if strings.HasPrefix(p, govPrefix) {
			out = append(out, p)
		}
	}
	return out
}

// A1: the default monoagentcli binary links no governor package at all; the tagged build does (the positive control that the
// check can see them).
func TestDefaultBinaryHasNoGovernorPackage(t *testing.T) {
	root := repoRoot(t)
	if got := governorPkgs(goList(t, root, "")); len(got) != 0 {
		t.Fatalf("the default monoagentcli links the governor: %v", got)
	}
	if got := governorPkgs(goList(t, root, "doomsday")); len(got) == 0 {
		t.Fatal("the doomsday build links no governor package: the guard cannot see them")
	}
}

type srcFile struct {
	path    string // relative to the module root, slash separated
	imports []string
	tagged  bool // //go:build doomsday
	file    *ast.File
}

// goFiles parses every non-test Go file of the module's own code (internal, cmd, wails-app, scripts).
func goFiles(t *testing.T, root string) []srcFile {
	t.Helper()
	var out []srcFile
	fset := token.NewFileSet()
	for _, top := range []string{"internal", "cmd", "wails-app", "scripts"} {
		_ = filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			raw, rerr := os.ReadFile(p)
			if rerr != nil {
				t.Fatal(rerr)
			}
			f, perr := parser.ParseFile(fset, p, raw, parser.ParseComments)
			if perr != nil {
				return nil // a file that does not parse is not ours to judge
			}
			sf := srcFile{file: f}
			sf.path, _ = filepath.Rel(root, p)
			sf.path = filepath.ToSlash(sf.path)
			for _, im := range f.Imports {
				sf.imports = append(sf.imports, strings.Trim(im.Path.Value, `"`))
			}
			head := string(raw)
			if i := strings.Index(head, "\npackage "); i > 0 {
				head = head[:i]
			}
			sf.tagged = regexp.MustCompile(`(?m)^//go:build doomsday\s*$`).MatchString(head)
			out = append(out, sf)
			return nil
		})
	}
	return out
}

func importsGovernor(f srcFile) bool {
	for _, im := range f.imports {
		if strings.HasPrefix(im, govPrefix) {
			return true
		}
	}
	return false
}

// The files outside the governor tree that may import it: the integrator's, every one tagged. A8: no CLI verb, MCP tool,
// workflow node, pane binding, coder-host tool or store function imports the governor, so none can reach a mutator.
var allowedImporters = []string{
	"internal/cli/doomsday/governorlauncher.go",
	"internal/cli/doomsday/governortokens.go",
	"internal/cli/doomsday/governorverbs.go",
	"internal/cli/doomsday/governorwire.go",
}

func TestOnlyTheIntegratorsTaggedFilesImportTheGovernor(t *testing.T) {
	var got []string
	for _, f := range goFiles(t, repoRoot(t)) {
		if strings.HasPrefix(f.path, "internal/doomsday/governor/") || !importsGovernor(f) {
			continue
		}
		got = append(got, f.path)
		if !f.tagged {
			t.Errorf("%s imports the governor without //go:build doomsday: the default build would link it", f.path)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(allowedImporters, ",") {
		t.Fatalf("importers of the governor changed:\n got %v\nwant %v\ndecide the new importer here, in review", got, allowedImporters)
	}
}

// Who may call each state-changing method of the governor (by selector name), counted over the allowed importers. A caller
// elsewhere cannot exist (the importer guard); this pins which of the integrator's files may do what.
var mutatorCallers = map[string][]string{
	"Reserve":         nil, // only the scheduler reserves
	"MarkAdmitted":    nil,
	"CancelAll":       {"internal/cli/doomsday/governortokens.go"},
	"TryAdmit":        {"internal/cli/doomsday/governorlauncher.go"},
	"Enqueue":         {"internal/cli/doomsday/governorlauncher.go"},
	"ReportRateLimit": {"internal/cli/doomsday/governorwire.go"},
	"Release":         {"internal/cli/doomsday/governorwire.go"},
	"Reconcile":       {"internal/cli/doomsday/governorwire.go"},
	"Pump":            {"internal/cli/doomsday/governorwire.go"},
	"NotifyStop":      {"internal/cli/doomsday/governorwire.go"},
}

func TestMutatorCallersAreEnumerated(t *testing.T) {
	seen := map[string]map[string]bool{}
	for _, f := range goFiles(t, repoRoot(t)) {
		if strings.HasPrefix(f.path, "internal/doomsday/governor/") || !importsGovernor(f) {
			continue
		}
		ast.Inspect(f.file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if _, tracked := mutatorCallers[sel.Sel.Name]; tracked {
					if seen[sel.Sel.Name] == nil {
						seen[sel.Sel.Name] = map[string]bool{}
					}
					seen[sel.Sel.Name][f.path] = true
				}
			}
			return true
		})
	}
	for name, allowed := range mutatorCallers {
		var got []string
		for p := range seen[name] {
			got = append(got, p)
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(allowed, ",") {
			t.Errorf("callers of %s: got %v, want %v", name, got, allowed)
		}
	}
}

// The governor's own code imports nothing of the module outside its tree: it holds no token, no vault, no spend meter, no
// coder host, no session store. (Test files may import the doomsday store and storage for fixtures; this checks the product code.)
func TestGovernorCodeImportsNothingThatCouldHoldAuthority(t *testing.T) {
	for _, f := range goFiles(t, repoRoot(t)) {
		if !strings.HasPrefix(f.path, "internal/doomsday/governor/") {
			continue
		}
		for _, im := range f.imports {
			if strings.HasPrefix(im, modRoot) && !strings.HasPrefix(im, govPrefix) {
				t.Errorf("%s imports %s: the governor must not reach tokens, the vault, spend or the host", f.path, im)
			}
		}
	}
}

// A8 by construction: no exported function or method of the governor is a setter of a cap, a limit or a threshold. The
// thresholds are compile-time constants; caps come from the verified charter row passed in as Limits.
func TestNoExportedSetterOfCapsOrLimits(t *testing.T) {
	deny := regexp.MustCompile(`^(Set|Raise|Increase|Grow|Override|Configure|Widen)`)
	for _, f := range goFiles(t, repoRoot(t)) {
		if !strings.HasPrefix(f.path, "internal/doomsday/governor/") {
			continue
		}
		for _, d := range f.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.IsExported() && deny.MatchString(fd.Name.Name) {
				t.Errorf("%s: exported %s looks like a setter of a cap or limit", f.path, fd.Name.Name)
			}
		}
	}
}

// The integrator builds governor.Limits from the verified charter only.
func TestLimitsComeFromTheCharterOnly(t *testing.T) {
	root := repoRoot(t)
	for _, p := range []string{"internal/cli/doomsday/governorwire.go", "internal/cli/doomsday/governorverbs.go"} {
		raw, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "MaxConcurrent: c.Limits.MaxConcurrent") {
			t.Errorf("%s does not take the cap from the charter's max_concurrent", p)
		}
		if strings.Contains(string(raw), "os.Getenv") || strings.Contains(string(raw), "flag.") {
			t.Errorf("%s reads the environment or a flag: a governor setting must not exist", p)
		}
	}
}
