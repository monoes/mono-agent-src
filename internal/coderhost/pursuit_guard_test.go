package coderhost

import (
	"context"
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
)

// The pursuit loop is a new autonomous spend path: it may spend (a judge call, a wake-up of the lead) only while the
// person's started pursuit runs. These guards fix what it is allowed to touch.

func pursuitSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob("pursuit*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		out[p] = f
	}
	if len(out) < 3 {
		t.Fatalf("found %d pursuit files: the guard would pass on nothing", len(out))
	}
	return out
}

func TestThePursuitFilesImportNothingThatCarriesAuthority(t *testing.T) {
	allowed := map[string]bool{
		"context": true, "crypto/rand": true, "encoding/hex": true, "database/sql": true, "errors": true, "fmt": true, "math": true, "sort": true, "strconv": true,
		"strings": true, "sync": true, "sync/atomic": true, "time": true, "unicode/utf8": true,
		"github.com/monoes/mono-agent/internal/doomsday/strategy/epoch":  true,
		"github.com/monoes/mono-agent/internal/doomsday/strategy/judge":  true,
		"github.com/monoes/mono-agent/internal/doomsday/strategy/policy": true,
		"github.com/monoes/mono-agent/internal/doomsday/strategy/types":  true,
		"github.com/monoes/mono-agent/internal/missionpursuit":           true,
		"github.com/monoes/mono-agent/internal/missionpursuit/inputs":    true,
		"github.com/monoes/mono-agent/internal/missionpursuit/scorer":    true,
		"github.com/monoes/mono-agent/internal/missionpursuit/strategy":  true,
	}
	for name, f := range pursuitSources(t) {
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if !allowed[p] {
				t.Errorf("%s imports %s: decide the new import here, in review (the loop must carry no process, network, MCP, CLI, finish or ledger authority)", name, p)
			}
		}
	}
}

// constString is the value of a string expression made of literals and + (a SQL text built by concatenation).
func constString(e ast.Expr) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			v, err := strconv.Unquote(x.Value)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return constString(x.X)
	case *ast.BinaryExpr:
		if x.Op == token.ADD {
			l, lok := constString(x.X)
			r, rok := constString(x.Y)
			return l + r, lok && rok
		}
	}
	return "", false
}

// The loop writes its own state table and, through the one queue function, a wake-up command. It never writes tasks,
// sessions, goals, pursuits, questions or the strategy tables itself (the epoch engine writes the latter through the
// strategy store). Three nets: the texts of string literals AND of literals joined with + are scanned for write
// statements; no Exec call exists outside the two functions that may write; and the names of the writers of settings,
// budgets, queues, tokens and the pursuit policy appear nowhere.
func TestThePursuitLoopWritesOnlyItsOwnStateAndTheQueue(t *testing.T) {
	write := regexp.MustCompile(`(?is)\b(INSERT|UPDATE|DELETE|REPLACE|ALTER|DROP|CREATE)\b`)
	target := regexp.MustCompile(`(?is)\b(?:INTO|UPDATE|FROM|TABLE)\s+([a-z_]+)`)
	mayExec := map[string]bool{"savePursuitState": true, "EnqueueWithPursuit": true}
	denied := map[string]bool{"SaveSettings": true, "UpdateSettings": true, "SetBudget": true, "SaveCheckin": true, "Enqueue": true,
		"EnqueueWithCheckin": true, "IssueSessionToken": true, "ChargeChild": true, "RevokeSessionTokens": true,
		"Confirm": true, "Pause": true, "Resume": true, "Start": true, "Propose": true, "Set": true}
	readsOnly := map[string]bool{"missionpursuit.Get": true, "missionpursuit.GetPursuit": true, "strategy.LatestEpoch": true}
	var bad []string
	for name, f := range pursuitSources(t) {
		check := func(s string) {
			if !write.MatchString(s) {
				return
			}
			for _, m := range target.FindAllStringSubmatch(s, -1) {
				if m[1] != "mission_pursuit_state" && !strings.EqualFold(m[1], "set") {
					bad = append(bad, name+": "+strings.TrimSpace(s))
				}
			}
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit, *ast.BinaryExpr:
					if s, ok := constString(x.(ast.Expr)); ok {
						check(s)
					}
				case *ast.CallExpr:
					// a call on missionpursuit.NewStore(...) reads the goal and the pursuit: nothing that locks, starts or pauses
					if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
						if in, ok := sel.X.(*ast.CallExpr); ok {
							if f, ok := in.Fun.(*ast.SelectorExpr); ok && f.Sel.Name == "NewStore" {
								if pkg, ok := f.X.(*ast.Ident); ok && !readsOnly[pkg.Name+"."+sel.Sel.Name] {
									bad = append(bad, name+": "+fd.Name.Name+" calls "+pkg.Name+".NewStore(...)."+sel.Sel.Name)
								}
							}
						}
					}
				case *ast.SelectorExpr:
					switch {
					case denied[x.Sel.Name]:
						bad = append(bad, name+": "+fd.Name.Name+" uses "+x.Sel.Name)
					case (x.Sel.Name == "Exec" || x.Sel.Name == "ExecContext") && !mayExec[fd.Name.Name]:
						bad = append(bad, name+": "+fd.Name.Name+" calls "+x.Sel.Name)
					}
				}
				return true
			})
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("the pursuit loop writes outside its state table:\n%s", strings.Join(bad, "\n"))
	}
}

// EnqueueWithPursuit has one caller, in the loop; no other file of the repository calls it.
func TestEnqueueWithPursuitHasOneCaller(t *testing.T) {
	root := filepath.Join("..", "..")
	calls, visited := 0, 0
	for _, top := range []string{"internal", "cmd", "wails-app"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			visited++
			src, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			n := strings.Count(string(src), ".EnqueueWithPursuit(")
			if n > 0 && filepath.Base(p) != "pursuit_wake.go" {
				t.Errorf("%s calls EnqueueWithPursuit", p)
			}
			calls += n
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", top, err)
		}
	}
	if visited < 500 || calls != 1 {
		t.Fatalf("visited %d files, %d calls: want the whole tree and exactly one caller", visited, calls)
	}
}

// Every turn-id prefix the host defines for turns it starts itself is host-made (so that the person's own messages, which are
// the person actions of a pursuit, are told apart from them). A NEW prefix constant must be added to HostMade, or be listed
// here as not a turn id, in review.
func TestEveryTurnIdPrefixIsHostMadeOrListed(t *testing.T) {
	notTurnIDs := map[string]bool{"mat-": true, "mst-": true} // task and session token prefixes
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 50 {
		t.Fatalf("%d files, %v", len(files), err)
	}
	found := 0
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) || !strings.HasSuffix(n.Name, "Prefix") {
						continue
					}
					v, ok := constString(vs.Values[i])
					if !ok || !strings.HasSuffix(v, "-") || notTurnIDs[v] {
						continue
					}
					found++
					if !HostMade(v + "x") {
						t.Errorf("%s (%q) in %s is not in HostMade: a send with this prefix would count as the person acting", n.Name, v, p)
					}
				}
			}
		}
	}
	if found < 4 {
		t.Fatalf("found %d prefixes: the scan is not looking at the constants", found)
	}
}

// Nothing in the pursuit files registers a verb, a route or a tool.
func TestThePursuitFilesRegisterNothingCallable(t *testing.T) {
	for name, f := range pursuitSources(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				switch id.Name {
				case "Handle", "HandleFunc", "Listen", "AddTool", "RegisterTool", "AddCommand", "Command":
					if id.Name != "Command" { // Command is the coder host's own queue record
						t.Errorf("%s uses %s", name, id.Name)
					}
				}
			}
			return true
		})
	}
}

// Nothing builds a Settings flag or a model from the pursuit files: the loop is off unless Host.Pursuit is set, which
// only the host's constructor does.
func TestThePursuitIsOffUnlessTheHostIsGivenIt(t *testing.T) {
	h := &Host{}
	if h.Pursuit != nil {
		t.Fatal("a host has a pursuit by default")
	}
	h.pursuitIfDue(context.Background())
}
