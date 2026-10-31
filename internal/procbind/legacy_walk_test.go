package procbind

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Every place that ends a legacy token (a DELETE of agent_tokens or coder_session_tokens, or an UPDATE of them that sets revoked_at) must also
// revoke the v2 tokens in the same transaction (design section 7, decision K1), or carry a reviewed
// "procbind:legacy-only <reason>" annotation. This test walks the packages that touch those tables and fails
// for a function (or declaration) that has an SQL string ending a legacy token and neither a revocation nor an
// annotation. The annotations are listed below, so adding one is a visible diff.
//
// THIS GUARD IS A HEURISTIC. It reads call shapes and import paths from the syntax tree (no go/types): it cannot
// tell two methods of the same name on different receivers apart, and it cannot prove a selector selects the right
// tokens. Its job is to make a NEW site that ends a token impossible to add silently. The protection that the
// existing sites really revoke is the behavioural tests, which run each path against real v2 rows and a forced
// failure: agenttokens (TestLegacyRevocationAlsoRevokesTheV2Tokens, TestAFailingV2RevocationRollsTheLegacyChangeBack,
// TestAV2TokenWithItsOwnId...), coderhost (TestEveryLegacyTokenDeletionAlsoRevokesTheV2SessionTokens,
// TestSweepOrphansRevokesAV2TokenThatHasNoLegacyToken, TestAFailingV2Revocation...), ai
// (TestEveryFormOfDeleteRevokesTheV2TokensOfTheConversation, TestDeleteRollsBackWhenTheV2...), cli/profile
// (TestDeleteProfileRevokesItsV2Tokens, ...RollsBack...). A new site needs its own behavioural test too.

var legacyEnds = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+(agent_tokens|coder_session_tokens)\b|\bUPDATE\s+(agent_tokens|coder_session_tokens)\s+SET\s[^;]*?\brevoked_at\s*=`)

const legacyOnly = "procbind:legacy-only"

// procbindPath is the import path the identities below must resolve to.
const procbindPath = "github.com/monoes/mono-agent/internal/procbind"

// procbindNames are the local names under which f imports procbind (its own name, or an alias).
func procbindNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, im := range f.Imports {
		p, err := strconv.Unquote(im.Path.Value)
		if err != nil || p != procbindPath {
			continue
		}
		if im.Name != nil {
			names[im.Name.Name] = true
		} else {
			names["procbind"] = true
		}
	}
	return names
}

// revokerMethods are the names of the functions f declares that return a procbind.Revoker.
func revokerMethods(f *ast.File, pb map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 {
			continue
		}
		if sel, ok := fd.Type.Results.List[0].Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Revoker" {
			if id, ok := sel.X.(*ast.Ident); ok && pb[id.Name] {
				out[fd.Name.Name] = true
			}
		}
	}
	return out
}

// walked are the directories (relative to internal/) that may end a legacy token.
var walked = []string{"ai", "cli/chat", "cli/org", "cli/profile", "coderhost", "agenttokens"}

// reviewedLegacyOnly is every declaration that carries the annotation, with the reason it is safe.
var reviewedLegacyOnly = map[string]string{
	"agenttokens/mission.go:purgeExpiredMissions":            "an expired token is denied by its expiry; the v2 rows of expired tokens follow their own retention (3e)",
	"coderhost/sessiontoken.go:IssueSessionToken":            "the expiry purge only: the replace of the session's tokens is revoked with the v2 side",
	"ai/chat_events_conversations.go:deleteConversationInTx": "a bound API token has no v2 form: the v2 families are mission and session tokens",
	"cli/chat/chat_history.go:deleteGuard":                   "the v2 mission tokens of the same owners are revoked by the common delete body, deleteConversationInTx, whichever form of delete runs",
}

// scan returns the declarations of src that end a legacy token without a revocation or an annotation, the
// declarations that carry the annotation (with their text after the marker), and how many sites it saw.
//
// A site is reviewed in one of three exact ways (identities are read from the syntax tree, never from substrings):
//   - a "procbind:legacy-only <reason>" comment within three lines above the SQL string;
//   - the string sits in a function literal that is the callback of procbind.Revoker.Run (a composite literal of
//     that type, or a call of a method named revoker()), and that callback really returns selectors: some return
//     has a first result other than nil, and none is `return nil, nil`;
//   - the string sits in a function literal passed to a call of revokeBoth or revokeSessionTokens whose arguments
//     include a non-empty procbind.Selector literal;
//   - or the declaration calls procbind.RevokeTx with a selector that is a non-empty literal or a variable.
func scan(t *testing.T, path string, src []byte) (bad []string, annotated map[string]string, sites int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	annotated = map[string]string{}
	pb := procbindNames(f)
	revokers := revokerMethods(f, pb)
	type note struct {
		end    int
		reason string
	}
	var notes []note
	for _, cg := range f.Comments {
		text := cg.Text()
		if i := strings.Index(text, legacyOnly); i >= 0 {
			reason := text[i+len(legacyOnly):]
			if j := strings.IndexByte(reason, '\n'); j >= 0 {
				reason = reason[:j]
			}
			notes = append(notes, note{end: fset.Position(cg.End()).Line, reason: strings.TrimSpace(reason)})
		}
	}
	for _, d := range f.Decls {
		var name string
		switch d := d.(type) {
		case *ast.FuncDecl:
			name = d.Name.Name
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				if v, ok := sp.(*ast.ValueSpec); ok && len(v.Names) > 0 {
					name = v.Names[0].Name
				}
			}
		default:
			continue
		}
		declRevokes := false
		ast.Inspect(d, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && isPkgCall(c.Fun, pb, "RevokeTx") && len(c.Args) >= 4 && realSelector(c.Args[3]) {
				declRevokes = true
			}
			return true
		})
		unreviewed := false
		var stack []ast.Node
		ast.Inspect(d, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			lit, ok := constString(n)
			if !ok || !legacyEnds.MatchString(lit) {
				return true
			}
			sites++
			line := fset.Position(n.Pos()).Line
			noted := false
			for _, nt := range notes {
				if nt.end >= line-3 && nt.end <= line {
					noted = true
					annotated[filepath.Base(path)+":"+name] = nt.reason
				}
			}
			if !noted && !declRevokes && !revokedByEnclosingCall(stack, pb, revokers) {
				unreviewed = true
			}
			// the parts of a concatenation are not looked at again; undo the push this node made
			stack = stack[:len(stack)-1]
			return false
		})
		if unreviewed {
			bad = append(bad, name)
		}
	}
	return bad, annotated, sites
}

// revokedByEnclosingCall says whether the innermost function literal on the stack is a revoking callback (see scan).
func revokedByEnclosingCall(stack []ast.Node, pb, revokers map[string]bool) bool {
	for i := len(stack) - 1; i > 0; i-- {
		fl, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		call, ok := stack[i-1].(*ast.CallExpr)
		if !ok {
			return false
		}
		isArg := false
		for _, a := range call.Args {
			isArg = isArg || a == ast.Expr(fl)
		}
		if !isArg {
			return false
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" && isRevokerValue(sel.X, pb, revokers) {
			return callbackReturnsSelectors(fl)
		}
		if name := calleeName(call.Fun); name == "revokeBoth" || name == "revokeSessionTokens" {
			for _, a := range call.Args {
				if cl, ok := a.(*ast.CompositeLit); ok && isSelectorType(cl.Type, pb) && len(cl.Elts) > 0 {
					return true
				}
			}
		}
		return false
	}
	return false
}

func calleeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// isPkgCall says whether e is P.name where P is a local name of procbind.
func isPkgCall(e ast.Expr, pb map[string]bool, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && pb[id.Name]
}

// isSelectorType says whether e is the type procbind.Selector (or a slice of it).
func isSelectorType(e ast.Expr, pb map[string]bool) bool {
	if at, ok := e.(*ast.ArrayType); ok {
		e = at.Elt
	}
	return isPkgCall(e, pb, "Selector")
}

// isRevokerValue says whether e is a procbind.Revoker literal or a call of a function this file declares as
// returning a procbind.Revoker.
func isRevokerValue(e ast.Expr, pb, revokers map[string]bool) bool {
	switch e := e.(type) {
	case *ast.CompositeLit:
		return isPkgCall(e.Type, pb, "Revoker")
	case *ast.CallExpr:
		return revokers[calleeName(e.Fun)]
	}
	return false
}

// realSelector says whether e names a selector that selects something: a non-empty literal, or a variable (a range
// variable over the selectors), never nil and never an empty literal.
func realSelector(e ast.Expr) bool {
	switch e := e.(type) {
	case *ast.CompositeLit:
		return len(e.Elts) > 0
	case *ast.Ident:
		return e.Name != "nil"
	}
	return false
}

// callbackReturnsSelectors says whether a Revoker.Run callback hands selectors back: some return has a first
// result that is neither nil nor an empty literal, and none returns nil, nil (no selectors and no error: revoke
// nothing, say nothing).
func callbackReturnsSelectors(fl *ast.FuncLit) bool {
	real, nothing := false, false
	ast.Inspect(fl.Body, func(m ast.Node) bool {
		if m == nil {
			return true
		}
		if inner, ok := m.(*ast.FuncLit); ok && inner != fl {
			return false
		}
		r, ok := m.(*ast.ReturnStmt)
		if !ok || len(r.Results) < 2 {
			return true
		}
		if isNil(r.Results[0]) && isNil(r.Results[1]) {
			nothing = true
		}
		if !isNil(r.Results[0]) && !emptyLiteral(r.Results[0]) {
			real = true
		}
		return true
	})
	return real && !nothing
}

func isNil(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

func emptyLiteral(e ast.Expr) bool {
	cl, ok := e.(*ast.CompositeLit)
	return ok && len(cl.Elts) == 0
}

// constString is the value of n when it is a string literal or a + of string literals.
func constString(n ast.Node) (string, bool) {
	switch n := n.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(n.Value)
		return v, err == nil
	case *ast.BinaryExpr:
		if n.Op != token.ADD {
			return "", false
		}
		l, lok := constString(n.X)
		r, rok := constString(n.Y)
		return l + r, lok && rok
	}
	return "", false
}

func TestEveryLegacyTokenEndIsRevokedInV2OrReviewed(t *testing.T) {
	found := map[string]string{}
	sites := 0
	for _, dir := range walked {
		entries, err := os.ReadDir(filepath.Join("..", dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join("..", dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			bad, annotated, n := scan(t, e.Name(), src)
			sites += n
			for _, b := range bad {
				t.Errorf("%s/%s: %s ends a legacy token without a v2 revocation (RevokeTx / procbind.Revoker) or a %q annotation with a reason", dir, e.Name(), b, legacyOnly)
			}
			for k, v := range annotated {
				found[dir+"/"+k] = v
			}
		}
	}
	if sites < 8 {
		t.Errorf("the walk saw only %d legacy-ending SQL strings; it is not looking at the right code", sites)
	}
	var got, want []string
	for k, v := range found {
		got = append(got, k)
		if len(v) < 20 {
			t.Errorf("%s: the %s annotation needs a stated reason", k, legacyOnly)
		}
	}
	for k := range reviewedLegacyOnly {
		want = append(want, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the legacy-only annotations changed:\n got  %v\n want %v\n(a new annotation needs review: add it to reviewedLegacyOnly with its reason)", got, want)
	}
	// the profile delete deletes by table name (dynamic SQL), so it is checked by name
	src, err := os.ReadFile(filepath.Join("..", "cli", "profile", "edit.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !callsRealRevokeTx(t, "edit.go", src, "deleteProfile") {
		t.Error("deleteProfile does not revoke the profile's v2 tokens")
	}
}

// callsRealRevokeTx says whether the named function calls procbind.RevokeTx with a selector that selects something.
func callsRealRevokeTx(t *testing.T, path string, src []byte, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	pb := procbindNames(f)
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != name {
			continue
		}
		found := false
		ast.Inspect(fd, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok && isPkgCall(c.Fun, pb, "RevokeTx") && len(c.Args) >= 4 && realSelector(c.Args[3]) {
				found = true
			}
			return true
		})
		return found
	}
	t.Fatalf("no function %s in %s", name, path)
	return false
}

// The walk itself: a function that ends a legacy token without a revocation is flagged, one with a revocation or a
// reviewed annotation is not, and a v2 table is not mistaken for a legacy one.
func TestTheLegacyWalkFlagsWhatItShould(t *testing.T) {
	src := []byte("package x\n" +
		"import (\n\"github.com/monoes/mono-agent/internal/procbind\"\nother \"example.com/other\"\n)\n" +
		"func (s *S) revoker() procbind.Revoker { return procbind.Revoker{} }\n" +
		"func bad() { db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n" +
		"func badDelete() { db.Exec(`DELETE FROM coder_session_tokens WHERE a = 1`) }\n" +
		"func good() { procbind.RevokeTx(ctx, db, k, procbind.Selector{A: 1}, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n" +
		"func rangeGood() { for _, sel := range sels { procbind.RevokeTx(ctx, db, k, sel, \"r\", now) }; db.Exec(`UPDATE agent_tokens SET revoked_at = 2`) }\n" +
		"func nilSelector() { procbind.RevokeTx(ctx, db, k, nil, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 3`) }\n" +
		"func emptySelector() { procbind.RevokeTx(ctx, db, k, procbind.Selector{}, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 4`) }\n" +
		"func otherPkg() { other.RevokeTx(ctx, db, k, sel, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 5`) }\n" +
		"func runGood() { procbind.Revoker{}.Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE a`); return []procbind.Selector{sel}, nil }) }\n" +
		"func runNothing() { s.revoker().Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE b`); return nil, nil }) }\n" +
		"func runErrorOnly() { s.revoker().Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE c`); return nil, err }) }\n" +
		"func runIndirect() { s.revoker().Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE d`); return sels, nil }) }\n" +
		"func emptySlice() { s.revoker().Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE i`); return []procbind.Selector{}, nil }) }\n" +
		"func otherRevoker() { other.Revoker{}.Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE j`); return sels, nil }) }\n" +
		"func otherSelector() { s.revokeBoth(ctx, other.Selector{Owner: o}, func(c *sql.Conn) error { c.Exec(`DELETE FROM agent_tokens WHERE k`); return nil }) }\n" +
		"func unknownRevoker() { z.somethingElse().Run(ctx, kinds, sc, func(c *sql.Conn) ([]procbind.Selector, error) { c.Exec(`DELETE FROM agent_tokens WHERE l`); return sels, nil }) }\n" +
		"func bothGood() { s.revokeBoth(ctx, procbind.Selector{Owner: o}, func(c *sql.Conn) error { c.Exec(`DELETE FROM agent_tokens WHERE e`); return nil }) }\n" +
		"func bothEmpty() { s.revokeBoth(ctx, procbind.Selector{}, func(c *sql.Conn) error { c.Exec(`DELETE FROM agent_tokens WHERE f`); return nil }) }\n" +
		"func notACallback() { f := func() { c.Exec(`DELETE FROM agent_tokens WHERE g`) }; s.revokeBoth(ctx, procbind.Selector{Owner: o}, nil); f() }\n" +
		"func ownName() { c.Exec(`DELETE FROM agent_tokens WHERE h`) }\nfunc revokeBoth() {}\n" +
		"// procbind:legacy-only an expired token is denied by its expiry\n" +
		"const purge = `DELETE FROM agent_tokens WHERE x`\n" +
		"func mixed() {\n// procbind:legacy-only the purge only, expired tokens are denied by expiry\ndb.Exec(`DELETE FROM agent_tokens WHERE expired`)\nx := 1\ny := 2\nz := 3\ndb.Exec(`DELETE FROM coder_session_tokens WHERE b = 1`) }\n" +
		"// procbind:legacy-only concatenated and reviewed, the expired ones only\nconst joined = `DEL` + `ETE ` + `FROM agent_tokens WHERE e`\n" +
		"func v2() { db.Exec(`UPDATE agent_tokens_v2 SET a = 1`) }\n" +
		"func bind() { db.Exec(`UPDATE coder_session_tokens SET pid = 1 WHERE a = 2`) }\n" +
		"func read() { db.Exec(`SELECT * FROM agent_tokens`) }\n")
	bad, annotated, sites := scan(t, "x.go", src)
	// good, rangeGood, runGood, runIndirect and bothGood are reviewed; everything else is flagged. In particular a
	// RevokeTx with a nil or empty selector, another package's RevokeTx, a callback that returns no selectors, and a
	// literal that merely sits near (or in a function called) revokeBoth are not enough.
	if want := "bad,badDelete,nilSelector,emptySelector,otherPkg,runNothing,runErrorOnly,emptySlice,otherRevoker,otherSelector,unknownRevoker,bothEmpty,notACallback,ownName,mixed"; strings.Join(bad, ",") != want {
		t.Errorf("flagged %v\nwant    %v", bad, want)
	}
	if len(annotated) != 3 || annotated["x.go:purge"] == "" || annotated["x.go:mixed"] == "" || annotated["x.go:joined"] == "" {
		t.Errorf("annotations %v", annotated)
	}
	if sites != 23 {
		t.Errorf("saw %d sites, want 23", sites)
	}
}

// The identities are resolved through the file's imports: an alias of procbind counts, a bare name that is not the
// procbind import (or a different package with the same name) does not.
func TestTheLegacyWalkResolvesImports(t *testing.T) {
	aliased := []byte("package x\nimport pb \"github.com/monoes/mono-agent/internal/procbind\"\n" +
		"func ok() { pb.RevokeTx(ctx, db, k, pb.Selector{A: 1}, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n")
	if bad, _, sites := scan(t, "a.go", aliased); len(bad) != 0 || sites != 1 {
		t.Errorf("an aliased procbind import was not recognised: %v (%d sites)", bad, sites)
	}
	notImported := []byte("package x\n" +
		"func no() { procbind.RevokeTx(ctx, db, k, procbind.Selector{A: 1}, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n")
	if bad, _, _ := scan(t, "n.go", notImported); len(bad) != 1 {
		t.Errorf("a procbind name that the file does not import was accepted: %v", bad)
	}
	wrongPath := []byte("package x\nimport \"example.com/elsewhere/procbind\"\n" +
		"func no() { procbind.RevokeTx(ctx, db, k, procbind.Selector{A: 1}, \"r\", now); db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n")
	if bad, _, _ := scan(t, "w.go", wrongPath); len(bad) != 1 {
		t.Errorf("another package named procbind was accepted: %v", bad)
	}
}

// IssuanceReady may be true only while the guard walk exists and the common conversation delete revokes the v2
// side: without them a legacy path could end a token and leave its v2 twin live.
func TestIssuanceStaysOffUntilTheRevocationGuardsExist(t *testing.T) {
	if !IssuanceReady {
		return
	}
	if _, err := os.Stat("legacy_walk_test.go"); err != nil {
		t.Fatal("IssuanceReady is true but the source-walk guard test file is missing")
	}
	src, err := os.ReadFile(filepath.Join("..", "ai", "chat_events_conversations.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !callsRealRevokeTx(t, "chat_events_conversations.go", src, "deleteConversationInTx") {
		t.Fatal("IssuanceReady is true but deleteConversationInTx does not revoke the v2 tokens")
	}
	for _, site := range productionRowKeyCalls(t) {
		if !rowKeyMintSites[site] {
			t.Errorf("IssuanceReady is true but %s calls RowKey (get-or-create, cached): a verification path must use RowKeyIfExists; if this is a mint, add it to rowKeyMintSites after review", site)
		}
	}
}

// rowKeyMintSites are the production FUNCTIONS (file relative to internal/, then the function) that may call RowKey:
// the mint only. None yet. A whole file is never allowed, so a verifier added next to a mint cannot hide in it.
var rowKeyMintSites = map[string]bool{}

// productionRowKeyCalls lists, as "file:Function", the functions in non-test files under internal/ that call
// procbind.RowKey (or, inside procbind, RowKey).
func productionRowKeyCalls(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, _ := filepath.Rel("..", path)
		for _, fn := range callsRowKey(t, path, src) {
			out = append(out, filepath.ToSlash(rel)+":"+fn)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// callsRowKey returns the names of the functions of src that call procbind.RowKey (resolved through its imports), or
// the bare RowKey in the procbind package.
func callsRowKey(t *testing.T, path string, src []byte) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	pb := procbindNames(f)
	var out []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		found := false
		ast.Inspect(fd, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isPkgCall(c.Fun, pb, "RowKey") || isPkgCall(c.Fun, pb, "AnswerKey") {
				found = true
			}
			if id, ok := c.Fun.(*ast.Ident); ok && (id.Name == "RowKey" || id.Name == "AnswerKey") && f.Name.Name == "procbind" {
				found = true
			}
			return true
		})
		if found {
			out = append(out, fd.Name.Name)
		}
	}
	return out
}

// The RowKey detector names the calling function: a qualified call, an aliased call and a bare call inside procbind are
// found, including a probe added next to the definition; RowKeyIfExists and another package's RowKey are not.
func TestTheRowKeyDetectorFindsOnlyRowKeyCalls(t *testing.T) {
	cases := map[string]struct {
		src  string
		want string
	}{
		"qualified": {"package x\nimport \"github.com/monoes/mono-agent/internal/procbind\"\nfunc f() { procbind.RowKey(ctx, db, p) }\n", "f"},
		"aliased":   {"package x\nimport pb \"github.com/monoes/mono-agent/internal/procbind\"\nfunc f() { pb.RowKey(ctx, db, p) }\n", "f"},
		"inside":    {"package procbind\nfunc f() { RowKey(ctx, db, p) }\n", "f"},
		// the definition's own file: the definition does not call RowKey, a probe next to it does
		"probe beside the definition": {"package procbind\nfunc RowKey(ctx, db, p) { return secrets.DeriveKey(ctx, db, p, purpose) }\nfunc verifierProbe(ctx, db, p) { return RowKey(ctx, db, p) }\n", "verifierProbe"},
		"definition alone":            {"package procbind\nfunc RowKey(ctx, db, p) { return secrets.DeriveKey(ctx, db, p, purpose) }\n", ""},
		"read-only":                   {"package x\nimport \"github.com/monoes/mono-agent/internal/procbind\"\nfunc f() { procbind.RowKeyIfExists(ctx, db, p) }\n", ""},
		"elsewhere":                   {"package x\nimport \"example.com/other\"\nfunc f() { other.RowKey(ctx, db, p) }\n", ""},
		"answer key qualified":        {"package x\nimport \"github.com/monoes/mono-agent/internal/procbind\"\nfunc f() { procbind.AnswerKey(ctx, db, p, g) }\n", "f"},
		"answer key inside":           {"package procbind\nfunc f() { AnswerKey(ctx, db, p, g) }\n", "f"},
		"answer key if exists":        {"package x\nimport \"github.com/monoes/mono-agent/internal/procbind\"\nfunc f() { procbind.AnswerKeyIfExists(ctx, db, p, g) }\n", ""},
	}
	for name, c := range cases {
		if got := strings.Join(callsRowKey(t, name+".go", []byte(c.src)), ","); got != c.want {
			t.Errorf("%s: found [%s], want [%s]", name, got, c.want)
		}
	}
	// today no production function calls RowKey, so turning issuance on would not trip it
	if sites := productionRowKeyCalls(t); len(sites) != 0 {
		t.Logf("production RowKey call sites (allowed only for the mint once issuance is on): %v", sites)
	}
}
