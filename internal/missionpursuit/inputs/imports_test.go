package inputs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch"
	"github.com/monoes/mono-agent/internal/doomsday/strategy/policy"
)

// The assertions live in a test file: a non-test `var _ epoch.RunState` would make this package an importer of the strategy
// libraries, which the doomsday strategy imports test allows only to the named wiring files.
var (
	_ epoch.RunState  = SessionRun{}
	_ policy.RunState = (*Reader)(nil)
	_ Inputs          = (*Reader)(nil)
)

func sources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[p] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no source files found")
	}
	return out
}

// This package reads host rows and nothing else: no process, no network, no authority, no storage package, no tasks store
// (whose writers it must not be able to reach), and from the doomsday strategy libraries only the plain types.
func TestThePackageImportsNothingThatCarriesAuthority(t *testing.T) {
	forbidden := []string{"os/exec", "net/http", "net", "plugin", "unsafe", "syscall",
		"/internal/storage", "/internal/secrets", "/internal/vault", "/internal/agenttokens", "/internal/coderhost", "/internal/cli",
		"/internal/mcp", "/internal/tasks", "/internal/dynorg", "/internal/doomsday/store", "/internal/doomsday/stop",
		"/internal/doomsday/finish", "/internal/doomsday/strategy/sstore"}
	for p := range sources(t) {
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatal(perr)
		}
		for _, im := range f.Imports {
			path, _ := strconv.Unquote(im.Path.Value)
			for _, bad := range forbidden {
				if path == bad || (strings.HasPrefix(bad, "/") && strings.Contains(path, bad)) {
					t.Errorf("%s imports %s", p, path)
				}
			}
			if strings.Contains(path, "internal/doomsday/strategy/") && !strings.HasSuffix(path, "/strategy/types") {
				t.Errorf("%s imports %s: only the plain types may be imported", p, path)
			}
			// the mission strategy store is allowed for reading; its writers are never called (see the next test)
		}
	}
}

var writeSQL = regexp.MustCompile(`(?i)\b(insert|update|delete|replace|drop|alter|create|pragma|attach|vacuum)\b`)

// Nothing here writes: no Exec call, no write keyword in ANY string literal (raw or interpreted, anywhere in the file), and
// none of the strategy store's writers.
func TestThePackageHasNoWriter(t *testing.T) {
	writers := []string{".Exec(", ".ExecContext(", "BeginTx(", ".Begin(", "InsertEpoch", "RecordJudgment", "MoveEpoch", "AcceptProposal",
		"MoveDirection", "RecordRefusal"}
	for p, src := range sources(t) {
		for _, w := range writers {
			if strings.Contains(src, w) {
				t.Errorf("%s calls %s", p, w)
			}
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, src, 0)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, uerr := strconv.Unquote(lit.Value); uerr == nil && writeSQL.MatchString(v) {
					t.Errorf("%s: string literal with a write keyword: %s", p, lit.Value)
				}
			}
			return true
		})
	}
}

// Only named wiring files import this package; today there are none (the host loop, a later slice, is decided in review).
func TestOnlyTheWiringFilesImportThePackage(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	var got []string
	visited := 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		werr := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			visited++
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/missionpursuit/inputs/") {
				return nil
			}
			f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
			if perr != nil {
				return perr
			}
			for _, im := range f.Imports {
				if path, _ := strconv.Unquote(im.Path.Value); strings.HasSuffix(path, "/internal/missionpursuit/inputs") {
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
	if visited == 0 {
		t.Fatal("the walk visited no Go files: the guard would pass on nothing")
	}
	sort.Strings(got)
	// the pursuit loop (T6, S4) is the one caller: its run state, snapshots and wake-up gate
	if want := []string{"internal/coderhost/pursuit.go", "internal/coderhost/pursuit_check.go", "internal/coderhost/pursuit_check_gate.go", "internal/coderhost/pursuit_check_meter.go", "internal/coderhost/pursuit_check_wake.go", "internal/coderhost/pursuit_facts.go", "internal/coderhost/pursuit_propose.go", "internal/coderhost/pursuit_recheck.go", "internal/coderhost/pursuit_wake.go"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("importers of the mission pursuit inputs changed: %v\ndecide the new importer here, in review", got)
	}
}
