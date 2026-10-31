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

var legacyEnds = regexp.MustCompile(`(?is)\bDELETE\s+FROM\s+(agent_tokens|coder_session_tokens)\b|\bUPDATE\s+(agent_tokens|coder_session_tokens)\s+SET\s[^;]*?\brevoked_at\s*=`)

// revocationMarks are the names whose presence in a declaration means it revokes the v2 side (or is the shared
// helper that does).
var revocationMarks = []string{"RevokeTx", "Revoker", "revokeBoth", "revokeSessionTokens", "sweepOrphanTokens"}

const legacyOnly = "procbind:legacy-only"

// walked are the directories (relative to internal/) that may end a legacy token.
var walked = []string{"ai", "cli/chat", "cli/org", "cli/profile", "coderhost", "agenttokens"}

// reviewedLegacyOnly is every declaration that carries the annotation, with the reason it is safe.
var reviewedLegacyOnly = map[string]string{
	"agenttokens/mission.go:purgeExpiredMissions":            "an expired token is denied by its expiry; the v2 rows of expired tokens follow their own retention (3e)",
	"coderhost/sessiontoken.go:IssueSessionToken":            "the expiry purge only: the replace of the session's tokens is revoked with the v2 side",
	"ai/chat_events_conversations.go:deleteConversationInTx": "a bound API token has no v2 form: the v2 families are mission and session tokens",
	"cli/chat/chat_history.go:deleteGuard":                   "the v2 mission tokens of the same owners are revoked by the common delete body, deleteConversationInTx, whichever form of delete runs",
}

// scan returns the declarations of src that end a legacy token without a revocation or an annotation, and the
// declarations that carry the annotation (with their text after the marker), and how many sites it saw.
func scan(t *testing.T, path string, src []byte) (bad []string, annotated map[string]string, sites int) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	annotated = map[string]string{}
	// the annotation comments: the line each ends on, and what follows the marker
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
		var doc *ast.CommentGroup
		switch d := d.(type) {
		case *ast.FuncDecl:
			name, doc = d.Name.Name, d.Doc
		case *ast.GenDecl:
			doc = d.Doc
			for _, sp := range d.Specs {
				if v, ok := sp.(*ast.ValueSpec); ok && len(v.Names) > 0 {
					name = v.Names[0].Name
				}
			}
		default:
			continue
		}
		start := d.Pos()
		if doc != nil {
			start = doc.Pos()
		}
		text := string(src[fset.Position(start).Offset:fset.Position(d.End()).Offset])
		revokes := false
		for _, m := range revocationMarks {
			revokes = revokes || strings.Contains(text, m)
		}
		unreviewed := false
		ast.Inspect(d, func(n ast.Node) bool {
			// a string literal, or a concatenation of string literals (read as the one string it makes)
			lit, ok := constString(n)
			if !ok || !legacyEnds.MatchString(lit) {
				return true
			}
			sites++
			line := fset.Position(n.Pos()).Line
			noted := false
			for _, nt := range notes {
				// an annotation covers the SQL string it sits directly above (within three lines)
				if nt.end >= line-3 && nt.end <= line {
					noted = true
					annotated[filepath.Base(path)+":"+name] = nt.reason
				}
			}
			if !noted && !revokes {
				unreviewed = true
			}
			return false // the parts of a concatenation are not looked at again
		})
		if unreviewed {
			bad = append(bad, name)
		}
	}
	return bad, annotated, sites
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
	if !strings.Contains(funcText(t, "edit.go", src, "deleteProfile"), "RevokeTx") {
		t.Error("deleteProfile does not revoke the profile's v2 tokens")
	}
}

func funcText(t *testing.T, path string, src []byte, name string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return string(src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset])
		}
	}
	t.Fatalf("no function %s in %s", name, path)
	return ""
}

// The walk itself: a function that ends a legacy token without a revocation is flagged, one with a revocation or a
// reviewed annotation is not, and a v2 table is not mistaken for a legacy one.
func TestTheLegacyWalkFlagsWhatItShould(t *testing.T) {
	src := []byte("package x\n" +
		"func bad() { db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n" +
		"func badDelete() { db.Exec(`DELETE FROM coder_session_tokens WHERE a = 1`) }\n" +
		"func good() { procbind.RevokeTx(); db.Exec(`UPDATE agent_tokens SET revoked_at = 1`) }\n" +
		"// procbind:legacy-only an expired token is denied by its expiry\n" +
		"const purge = `DELETE FROM agent_tokens WHERE x`\n" +
		"func mixed() {\n// procbind:legacy-only the purge only, expired tokens are denied by expiry\ndb.Exec(`DELETE FROM agent_tokens WHERE expired`)\nx := 1\ny := 2\nz := 3\ndb.Exec(`DELETE FROM coder_session_tokens WHERE b = 1`) }\n" +
		"// procbind:legacy-only concatenated and reviewed, the expired ones only\nconst joined = `DEL` + `ETE ` + `FROM agent_tokens WHERE e`\n" +
		"func v2() { db.Exec(`UPDATE agent_tokens_v2 SET a = 1`) }\n" +
		"func bind() { db.Exec(`UPDATE coder_session_tokens SET pid = 1 WHERE a = 2`) }\n" +
		"func read() { db.Exec(`SELECT * FROM agent_tokens`) }\n")
	bad, annotated, sites := scan(t, "x.go", src)
	if strings.Join(bad, ",") != "bad,badDelete,mixed" {
		t.Errorf("flagged %v, want bad, badDelete and mixed", bad)
	}
	if len(annotated) != 3 || annotated["x.go:purge"] == "" || annotated["x.go:mixed"] == "" || annotated["x.go:joined"] == "" {
		t.Errorf("annotations %v", annotated)
	}
	if sites != 7 {
		t.Errorf("saw %d sites, want 7", sites)
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
	if !strings.Contains(funcText(t, "chat_events_conversations.go", src, "deleteConversationInTx"), "procbind.RevokeTx(") {
		t.Fatal("IssuanceReady is true but deleteConversationInTx does not revoke the v2 tokens")
	}
}
