package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/missionpursuit"
)

type srcFile struct {
	path string
	file *ast.File
	fset *token.FileSet
}

// nonTestSources parses every non-test Go file under wails-app (sub-packages too).
func nonTestSources(t *testing.T) []srcFile {
	t.Helper()
	var out []srcFile
	_ = filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "node_modules" || d.Name() == "frontend" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		out = append(out, parseSource(t, path, nil))
		return nil
	})
	return out
}

func parseSource(t *testing.T, path string, src []byte) srcFile {
	t.Helper()
	fset := token.NewFileSet()
	var in any
	if src != nil {
		in = src
	}
	f, err := parser.ParseFile(fset, path, in, 0)
	if err != nil {
		t.Fatal(err)
	}
	return srcFile{path: path, file: f, fset: fset}
}

func strLit(n ast.Node) (string, bool) {
	if b, ok := n.(*ast.BasicLit); ok && b.Kind == token.STRING {
		if s, err := strconv.Unquote(b.Value); err == nil {
			return s, true
		}
	}
	return "", false
}

// literalGroups walks f and calls fn for every group of string literals that sit side by side (the elements of a composite
// literal, the arguments of a call), with the name of the function they are in.
func literalGroups(f srcFile, fn func(group []string, pos token.Pos, inFunc string)) {
	for _, decl := range f.file.Decls {
		name := ""
		if fd, ok := decl.(*ast.FuncDecl); ok {
			name = fd.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			var elts []ast.Expr
			switch v := n.(type) {
			case *ast.CompositeLit:
				elts = v.Elts
			case *ast.CallExpr:
				elts = v.Args
			default:
				return true
			}
			var g []string
			for _, e := range elts {
				if s, ok := strLit(e); ok {
					g = append(g, s)
				}
			}
			if len(g) > 0 {
				fn(g, n.Pos(), name)
			}
			return true
		})
	}
}

func has(group []string, words ...string) bool {
	for _, g := range group {
		for _, w := range words {
			if g == w {
				return true
			}
		}
	}
	return false
}

// beginViolations are the places in f that build a pursuit begin (or a goal confirm, start or set) outside the one function.
func beginViolations(f srcFile) []string {
	var out []string
	add := func(pos token.Pos, why string) { out = append(out, f.fset.Position(pos).String()+": "+why) }
	literalGroups(f, func(g []string, pos token.Pos, in string) {
		if has(g, "pursuit", "goal") {
			if has(g, "confirm", "start", "set", "propose", "lock") {
				add(pos, "a goal or pursuit verb that writes (confirm/start/set/propose)")
			}
			if has(g, "begin") && in != "pursuitBeginArgv" {
				add(pos, "pursuit begin outside pursuitBeginArgv")
			}
		}
		for _, s := range g {
			switch {
			case s == "begin" && in != "pursuitBeginArgv":
				add(pos, `the word "begin" outside pursuitBeginArgv`)
			case (strings.HasPrefix(s, "--goal-hash") || strings.HasPrefix(s, "--consent")) && in != "pursuitBeginArgv":
				add(pos, "a begin flag outside pursuitBeginArgv")
			case strings.HasPrefix(s, "--gui-phrase") && in != "pursuitBeginArgv" && in != "doomsdayCharterArgv":
				add(pos, "--gui-phrase outside the two start argv builders")
			}
		}
	})
	return out
}

func callsOf(root ast.Node, name string) (calls []token.Pos, uses int) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.Ident:
			if v.Name == name {
				uses++
			}
		case *ast.CallExpr:
			switch fun := v.Fun.(type) {
			case *ast.Ident:
				if fun.Name == name {
					calls = append(calls, v.Pos())
				}
			case *ast.SelectorExpr:
				if fun.Sel.Name == name {
					calls = append(calls, v.Pos())
				}
			}
		}
		return true
	})
	return
}

func funcBody(fs []srcFile, name string) (srcFile, *ast.FuncDecl) {
	for _, f := range fs {
		for _, d := range f.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
				return f, fd
			}
		}
	}
	return srcFile{}, nil
}

// The app begins a pursuit in one place: pursuitBeginArgv builds the argv, beginPursuit is its only caller, PursuitStart is
// beginPursuit's only caller, and there it comes after the native dialog and the spend of the grant.
func TestAppBuildsPursuitBeginOnlyThroughTheTokenPath(t *testing.T) {
	fs := nonTestSources(t)
	for _, f := range fs {
		for _, v := range beginViolations(f) {
			t.Error(v)
		}
	}
	// the chain of callers: exactly one at each step, none used as a value
	chain := [][2]string{{"pursuitBeginArgv", "beginPursuit"}, {"beginPursuit", "PursuitStart"}}
	for _, c := range chain {
		callee, caller := c[0], c[1]
		total, calls, inCaller := 0, 0, 0
		for _, f := range fs {
			cs, uses := callsOf(f.file, callee)
			total += uses
			calls += len(cs)
		}
		_, fd := funcBody(fs, caller)
		if fd == nil {
			t.Fatalf("no function %s", caller)
		}
		for _, f := range fs {
			if _, d := funcBody([]srcFile{f}, caller); d != nil {
				cs, _ := callsOf(d, callee)
				inCaller += len(cs)
			}
		}
		if calls != 1 || inCaller != 1 || total != 2 { // the declaration and the one call
			t.Errorf("%s: %d calls, %d inside %s, %d mentions; want exactly one call, inside %s, never a value", callee, calls, inCaller, caller, total, caller)
		}
	}
	// inside PursuitStart: the dialog, then the spend of the grant, then the begin
	_, fd := funcBody(fs, "PursuitStart")
	pos := func(name string) token.Pos {
		cs, _ := callsOf(fd, name)
		if len(cs) != 1 {
			t.Fatalf("PursuitStart calls %s %d times, want once", name, len(cs))
		}
		return cs[0]
	}
	if !(pos("peek") < pos("askNativeTitled") && pos("askNativeTitled") < pos("consume") && pos("consume") < pos("beginPursuit")) {
		t.Error("PursuitStart must peek the grant, ask in the native dialog, spend the grant, and only then begin")
	}
	if len(beginViolations(parseSource(t, "mutant.go", []byte(`package main
func other() { _ = []string{"task", "session", "pursuit", "begin", "5"} }`)))) == 0 {
		t.Error("the scan does not see a begin argv outside pursuitBeginArgv")
	}
	if len(beginViolations(parseSource(t, "mutant.go", []byte(`package main
func other() { _ = []string{"task", "session", "goal", "confirm", "5"} }`)))) == 0 {
		t.Error("the scan does not see a goal confirm")
	}
}

// The pursuit verbs the app may run are an allow-list: preview (the start file) and the reads and the person's pause and resume.
func TestPursuitReadBindingsUseAnAllowList(t *testing.T) {
	allowed := map[string]bool{"task": true, "session": true, "pursuit": true, "preview": true, "status": true, "list": true, "pause": true, "resume": true, "show": true, "begin": true}
	check := func(f srcFile) (bad []string) {
		literalGroups(f, func(g []string, pos token.Pos, in string) {
			if !has(g, "pursuit") {
				return
			}
			for _, s := range g {
				if !allowed[s] && !strings.HasPrefix(s, "-") {
					bad = append(bad, f.fset.Position(pos).String()+": pursuit "+strconv.Quote(s))
				}
			}
		})
		return
	}
	for _, f := range nonTestSources(t) {
		for _, b := range check(f) {
			t.Error(b)
		}
	}
	if len(check(parseSource(t, "mutant.go", []byte(`package main
func other() { _ = []string{"task", "session", "pursuit", "stop", "5"} }`)))) == 0 {
		t.Error("the allow-list does not refuse an unlisted verb")
	}
}

// The pursuit dialog goes through the one seam (askNativeTitled -> confirmNative -> showDialog): the pursuit files name neither
// the Wails dialog nor showDialog nor the real confirm, and only the start function asks.
func TestPursuitDialogGoesThroughTheSharedSeam(t *testing.T) {
	fs := nonTestSources(t)
	for _, f := range fs {
		base := filepath.Base(f.path)
		ast.Inspect(f.file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			switch id.Name {
			case "showDialog", "MessageDialog", "realConfirmNative", "dialogRun":
				if strings.HasPrefix(base, "app_pursuit_") {
					t.Errorf("%s: %s: the pursuit files ask only through askNativeTitled", f.fset.Position(id.Pos()), id.Name)
				}
			case "confirmNative":
				if !map[string]bool{"app_doomsday_grant.go": true, "app_doomsday_start.go": true, "e2e_host.go": true}[base] {
					t.Errorf("%s: confirmNative is used outside the dialog helpers", f.fset.Position(id.Pos()))
				}
			}
			return true
		})
		if strings.HasPrefix(base, "app_pursuit_") {
			if cs, _ := callsOf(f.file, "askNativeTitled"); len(cs) > 0 && base != "app_pursuit_start.go" {
				t.Errorf("%s asks in a dialog", base)
			}
		}
	}
}

// Both bindings are for the page only: no Go code of the app calls them, none names them as a string (reflection), and the
// App has no other Pursuit binding than these (a new one must come with its own guard).
func TestPursuitBindingsAreFrontendOnly(t *testing.T) {
	for _, f := range nonTestSources(t) {
		ast.Inspect(f.file, func(n ast.Node) bool {
			if s, ok := strLit(n); ok && strings.Contains(s, "PursuitStart") && !strings.Contains(s, " ") {
				t.Errorf("%s: a binding named as a string", f.fset.Position(n.Pos()))
			}
			return true
		})
		for _, name := range []string{"PursuitStart", "PursuitStartPreview"} {
			if cs, _ := callsOf(f.file, name); len(cs) > 0 {
				t.Errorf("%s: %s is called from Go; it is for the page", f.path, name)
			}
		}
	}
	var got []string
	typ := reflect.TypeOf(&App{})
	for i := 0; i < typ.NumMethod(); i++ {
		if strings.HasPrefix(typ.Method(i).Name, "Pursuit") {
			got = append(got, typ.Method(i).Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "PursuitStart,PursuitStartPreview" {
		t.Fatalf("Pursuit bindings: %v", got)
	}
}

// The browser end-to-end host answers a pursuit dialog with errNoNativeDialog before it reads any scripted answer: a start over
// HTTP can never be confirmed (a scripted yes is not a person's click). The host is a tagged file, so the source is read.
func TestTheE2EHostNeverConfirmsAPursuitStart(t *testing.T) {
	f := parseSource(t, "e2e_host.go", nil)
	_, fd := funcBody([]srcFile{f}, "e2eConfirm")
	if fd == nil || len(fd.Body.List) == 0 {
		t.Fatal("no e2eConfirm")
	}
	first, ok := fd.Body.List[0].(*ast.IfStmt)
	if !ok {
		t.Fatal("e2eConfirm must begin by refusing a pursuit dialog")
	}
	cond, ok := first.Cond.(*ast.BinaryExpr)
	if !ok || cond.Op != token.EQL || !mentions(cond, "title") || !mentions(cond, "pursuitDialogTitle") || !mentions(first.Body, "errNoNativeDialog") {
		t.Fatal("e2eConfirm's first statement must be: if title == pursuitDialogTitle { return false, errNoNativeDialog }")
	}
}

func mentions(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return true
	})
	return found
}

// The pursuit files carry no number of the CLI's: no default, no maximum, no dollar figure, no per-hour count. They are read from
// the real constants, so a changed default changes what is forbidden.
func TestNoNumberIsCopiedIntoTheApp(t *testing.T) {
	type num struct {
		v    float64
		what string
	}
	forbidden := []num{
		{float64(missionpursuit.DefaultParkedMax), "DefaultParkedMax"}, {float64(missionpursuit.MaxParkedMax), "MaxParkedMax"},
		{float64(missionpursuit.DefaultPct), "DefaultPct"}, {float64(missionpursuit.MaxPct), "MaxPct"},
		{float64(missionpursuit.DefaultWakesMax), "DefaultWakesMax"}, {float64(missionpursuit.MaxWakesMax), "MaxWakesMax"},
		{float64(missionpursuit.MaxHostWakesPerHour), "MaxHostWakesPerHour"}, {float64(missionpursuit.MaxHostJudgeCallsPerHour), "MaxHostJudgeCallsPerHour"},
		{coderhost.DefaultWakeCapUSD, "DefaultWakeCapUSD"}, {coderhost.MaxWakeCapUSD, "MaxWakeCapUSD"}, {coderhost.PursuitJudgeCallCapUSD, "PursuitJudgeCallCapUSD"},
		{0.5, "the judge call cap"}, {12, "a per-hour count"}, {30, "a per-hour count"}, {15, "the mission-hour figure"},
	}
	money := regexp.MustCompile(`\$[0-9%]`)
	scan := func(f srcFile) (bad []string) {
		// numbers that are not values: a shift count, an index, an array length, the base of a conversion
		skip := map[ast.Node]bool{}
		ast.Inspect(f.file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				if v.Op == token.SHL || v.Op == token.SHR {
					skip[v.Y] = true
				}
			case *ast.IndexExpr:
				skip[v.Index] = true
			case *ast.SliceExpr:
				skip[v.High], skip[v.Low] = true, true
			case *ast.ArrayType:
				skip[v.Len] = true
			case *ast.CallExpr:
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "FormatInt" || sel.Sel.Name == "ParseInt" || sel.Sel.Name == "ParseUint") && len(v.Args) > 1 {
					skip[v.Args[1]] = true
				}
			}
			return true
		})
		ast.Inspect(f.file, func(n ast.Node) bool {
			b, ok := n.(*ast.BasicLit)
			if !ok || skip[b] {
				return true
			}
			switch b.Kind {
			case token.INT, token.FLOAT:
				v, err := strconv.ParseFloat(strings.ReplaceAll(b.Value, "_", ""), 64)
				if err != nil {
					if iv, ierr := strconv.ParseInt(strings.ReplaceAll(b.Value, "_", ""), 0, 64); ierr == nil {
						v = float64(iv)
					}
				}
				for _, fb := range forbidden {
					if v == fb.v {
						bad = append(bad, f.fset.Position(b.Pos()).String()+": "+b.Value+" is "+fb.what)
					}
				}
			case token.STRING:
				if s, _ := strconv.Unquote(b.Value); money.MatchString(s) {
					bad = append(bad, f.fset.Position(b.Pos()).String()+": a dollar sign in a text")
				}
			}
			return true
		})
		return
	}
	n := 0
	for _, f := range nonTestSources(t) {
		if strings.HasPrefix(filepath.Base(f.path), "app_pursuit_") {
			n++
			for _, b := range scan(f) {
				t.Error(b)
			}
		}
	}
	if n < 3 {
		t.Fatalf("scanned %d pursuit files", n)
	}
	if len(scan(parseSource(t, "mutant.go", []byte("package main\nvar x = 20\nvar y = 0.50\nvar z = \"$2\"")))) < 3 {
		t.Error("the scan does not see a copied default, a copied cap or a dollar figure")
	}
}
