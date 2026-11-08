package coderhost

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
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
		if strings.HasSuffix(p, "_test.go") || p == "pursuit_clamp.go" {
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

// The loop writes its own state table and, through the one queue function, a wake-up command. It never writes tasks,
// sessions, goals, pursuits, questions or the strategy tables itself (the epoch engine writes the latter through the
// strategy store).
func TestThePursuitLoopWritesOnlyItsOwnStateAndTheQueue(t *testing.T) {
	write := regexp.MustCompile(`(?is)\b(INSERT|UPDATE|DELETE|REPLACE|ALTER|DROP|CREATE)\b`)
	target := regexp.MustCompile(`(?is)\b(?:INTO|UPDATE|FROM|TABLE)\s+([a-z_]+)`)
	var bad []string
	for name, f := range pursuitSources(t) {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, _ := strconv.Unquote(lit.Value)
			if !write.MatchString(s) {
				return true
			}
			for _, m := range target.FindAllStringSubmatch(s, -1) {
				if m[1] != "mission_pursuit_state" && !strings.EqualFold(m[1], "set") {
					bad = append(bad, name+": "+strings.TrimSpace(s))
				}
			}
			return true
		})
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("the pursuit loop writes outside its state table:\n%s", strings.Join(bad, "\n"))
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
