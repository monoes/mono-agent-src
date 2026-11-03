package coderhost

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The inventory backstop of note 13.4 section 6 and the T26 inventory: the enforcement functions of the open-ended
// mode are the only way to the effects they guard, none of them accepts the operator's scope, and a new call site
// fails here until it is added with its tests. The tests parse the source, as the note's section 8 prescribes.

func parseModule(t *testing.T, roots ...string) map[string]*ast.File {
	t.Helper()
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, root := range roots {
		_ = filepath.WalkDir(filepath.Join("..", "..", root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
			if perr != nil {
				t.Fatalf("%s: %v", p, perr)
			}
			out[filepath.ToSlash(p)] = f
			return nil
		})
	}
	if len(out) < 40 {
		t.Fatalf("the inventory saw %d files: it is not looking at the module", len(out))
	}
	return out
}

func callsOf(n ast.Node, name string) []token.Pos {
	var out []token.Pos
	ast.Inspect(n, func(x ast.Node) bool {
		if c, ok := x.(*ast.CallExpr); ok {
			switch f := c.Fun.(type) {
			case *ast.SelectorExpr:
				if f.Sel.Name == name {
					out = append(out, c.Pos())
				}
			case *ast.Ident:
				if f.Name == name {
					out = append(out, c.Pos())
				}
			}
		}
		return true
	})
	return out
}

func paramTypes(fd *ast.FuncDecl) []string {
	var out []string
	for _, p := range fd.Type.Params.List {
		switch t := p.Type.(type) {
		case *ast.Ident:
			out = append(out, t.Name)
		case *ast.SelectorExpr:
			out = append(out, t.Sel.Name)
		}
	}
	return out
}

// T26 (inventory): every host function that reaches the enforcement function takes the generic CallerScope, asks
// LeadMission (which refuses anything but a verified lead's scope) before it, and never names OperatorScope.
func TestEveryEnforcementCallSiteChecksTheLeadScopeFirst(t *testing.T) {
	files := parseModule(t, "internal/coderhost")
	var found []string
	for path, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			sites := enforcerCalls(fd.Body)
			if len(sites) == 0 {
				continue
			}
			found = append(found, fd.Name.Name)
			if !slices.Contains(paramTypes(fd), "CallerScope") || slices.Contains(paramTypes(fd), "OperatorScope") {
				t.Errorf("%s: %s must take the generic CallerScope and never OperatorScope: %v", path, fd.Name.Name, paramTypes(fd))
			}
			lead := callsOf(fd.Body, "LeadMission")
			if len(lead) == 0 || lead[0] > slices.Min(sites) {
				t.Errorf("%s: %s reaches the enforcement function without LeadMission first", path, fd.Name.Name)
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == "OperatorScope" {
					t.Errorf("%s: %s names OperatorScope", path, fd.Name.Name)
				}
				return true
			})
		}
	}
	slices.Sort(found)
	if want := []string{"BossAnswerAsk", "BossApprovePlan", "BossGrantBudget"}; !slices.Equal(found, want) {
		t.Fatalf("call sites of the enforcement function: %v, inventory %v: add the new one with its tests", found, want)
	}
}

// T26 (run): the operator's scope, and a scope that is not a lead's, reach none of them and spend nothing.
func TestNoEnforcementFunctionAcceptsTheOperatorsScope(t *testing.T) {
	f, l, _, kid, v := planFixture(t, planLim)
	ctx := context.Background()
	for _, sc := range []CallerScope{OperatorScope{}, LeadScope{}, NewLeadScope(Caller{ProfileID: "p", ConversationID: f.pc}, BindingNone+1)} {
		if _, err := f.st.BossApprovePlan(ctx, sc, "p", kid.ID, v.Rev, v.Digest); err == nil {
			t.Fatalf("%T approved a plan", sc)
		}
		if _, _, err := f.st.BossAnswerAsk(ctx, sc, "p", 1, ClassAskCritical, "yes"); err == nil {
			t.Fatalf("%T answered an ask", sc)
		}
		if _, err := f.st.BossGrantBudget(ctx, sc, "p", 1, 1_000_000); err == nil {
			t.Fatalf("%T granted budget", sc)
		}
	}
	if len(l.ids) != 0 {
		t.Fatalf("a refused scope spent an approval: %v", l.ids)
	}
}

// The mechanical boundary: the task-store entry points the boss uses are reached from the route and from nowhere
// else, and nothing outside package ops builds an ops.Authorization.
func TestTheBossEntryPointsHaveOneCallerEach(t *testing.T) {
	files := parseModule(t, "internal", "cmd", "wails-app")
	allowed := map[string][]string{
		"ApprovePlanAsBoss": {"internal/coderhost/doomsday_plan.go"},
		"PlanFactsForBoss":  {"internal/coderhost/doomsday_route.go"},
	}
	for path, f := range files {
		p := strings.TrimPrefix(filepath.ToSlash(path), "../../")
		if strings.HasPrefix(p, "internal/tasks/") {
			continue // the definitions
		}
		for name, ok := range allowed {
			if len(callsOf(f, name)) > 0 && !slices.Contains(ok, p) {
				t.Errorf("%s calls %s: only %v may", p, name, ok)
			}
		}
		if strings.HasPrefix(p, "internal/doomsday/ops/") {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if cl, ok := n.(*ast.CompositeLit); ok {
				if se, ok := cl.Type.(*ast.SelectorExpr); ok && se.Sel.Name == "Authorization" {
					if id, ok := se.X.(*ast.Ident); ok && id.Name == "ops" {
						t.Errorf("%s builds an ops.Authorization", p)
					}
				}
			}
			return true
		})
	}
}

// enforcerCalls are the calls of Decide or Authorize on a field named Enforcer, Typed or TypedEnforcer: the
// enforcement functions of package ops reached through the route.
func enforcerCalls(n ast.Node) []token.Pos {
	var out []token.Pos
	ast.Inspect(n, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		f, ok := c.Fun.(*ast.SelectorExpr)
		if !ok || (f.Sel.Name != "Decide" && f.Sel.Name != "Authorize") {
			return true
		}
		if r, ok := f.X.(*ast.SelectorExpr); ok && slices.Contains([]string{"Enforcer", "Typed", "TypedEnforcer"}, r.Sel.Name) {
			out = append(out, c.Pos())
		}
		return true
	})
	return out
}
