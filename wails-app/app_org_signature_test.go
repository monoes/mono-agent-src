package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
)

// fakeSignCLI is a stub monoagentcli for designer saves: validate is
// valid, reconcile-doc answers reconciled, and `org sign <org> --status`
// answers status (the CLI's verdict). Every call's argv is logged.
func fakeSignCLI(t *testing.T, reconciled, status string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a shell script (unix-only)")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\ncase \"$*\" in\n" +
		"  *reconcile-doc*) cat > /dev/null; printf '%s\\n' '" + reconciled + "' ;;\n" +
		"  *validate*) printf '%s\\n' '{\"v\":1,\"org\":\"growth\",\"valid\":true}' ;;\n" +
		"  *--status*) printf '%s\\n' '" + status + "' ;;\n" +
		"  *) printf '{\"ok\":true}\\n' ;;\nesac\n"
	bin := filepath.Join(dir, "monoagentcli")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return log
}

const (
	statusSigned      = `{"v":1,"org":"growth","supported":true,"state":"signed"}`
	statusUnsupported = `{"v":1,"org":"growth","supported":false}`
	statusMissing     = `{"error":"org \"growth\" not found","code":"not_found"}`
	reconciledRead    = `{"v":1,"org":{"name":"growth","goal":"grow","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"responsibilities":["lead"],"policy":{"git":"read"}}]},"reconcile":[]}`
)

// signedDesignOrg saves the two-role org and signs it the way monomind
// 2.21 would, in a scratch operator dir.
func signedDesignOrg(t *testing.T, root string) {
	t.Helper()
	t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", filepath.Join(t.TempDir(), "operator"))
	orgsigntest.AsOperator(t)
	if _, err := orgdesign.Save(root, twoRoleDoc()); err != nil {
		t.Fatal(err)
	}
	orgsigntest.Sign(t, root, "growth")
}

func callsWith(t *testing.T, log, part string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	var out []string
	for _, c := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.Contains(c, part) {
			out = append(out, c)
		}
	}
	return out
}

// #288: a designer save of a signed org is re-signed through the CLI, for
// the projection hash of exactly what it wrote.
func TestSaveOrgDoc_ResignsASignedOrg(t *testing.T) {
	log := fakeSignCLI(t, reconciledRead, statusSigned)
	a, root := newOrgDesignApp(t)
	signedDesignOrg(t, root)

	d, err := orgdesign.Load(root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveOrgDoc(root, d, false); err != nil {
		t.Fatalf("saveOrgDoc: %v", err)
	}
	raw, _, _ := orgsign.ReadFile(root, "growth")
	want, _ := orgsign.Hash(root, raw)
	expect := "--profile gui --json org --project " + root + " sign growth --yes --expect-hash " + want
	if calls := callsWith(t, log, "--yes"); len(calls) != 1 || calls[0] != expect {
		t.Fatalf("sign calls = %q, want %q", calls, expect)
	}
	if len(callsWith(t, log, "--status")) != 1 {
		t.Fatal("the CLI's verdict was not asked for before the write")
	}
}

// An org changed outside the app is never signed by a designer save, even
// when the CLI's verdict (stale, or wrong) says signed: the pin against the
// signature's hash fails.
func TestSaveOrgDoc_NeverSignsAnOutsideEdit(t *testing.T) {
	log := fakeSignCLI(t, reconciledRead, statusSigned)
	a, root := newOrgDesignApp(t)
	signedDesignOrg(t, root)
	outside := twoRoleDoc()
	outside.Runtime = "codex"
	if _, err := orgdesign.Save(root, outside); err != nil {
		t.Fatal(err)
	}
	d, err := orgdesign.Load(root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.saveOrgDoc(root, d, false); err != nil {
		t.Fatalf("saveOrgDoc: %v", err)
	}
	if calls := callsWith(t, log, "--yes"); len(calls) != 0 {
		t.Fatalf("signed an outside edit: %q", calls)
	}
}

// Only a new org the user created may be signed as theirs (signNew); the
// same new document saved any other way is left for review.
func TestSaveOrgDoc_SignsANewOrgOnlyWhenAsked(t *testing.T) {
	for _, signNew := range []bool{false, true} {
		log := fakeSignCLI(t, reconciledRead, statusMissing)
		a, root := newOrgDesignApp(t)
		t.Setenv("MONOMIND_ORGRT_OPERATOR_DIR", filepath.Join(t.TempDir(), "operator"))
		orgsigntest.AsOperator(t)
		a.orgSignSupport.set(true)
		if _, err := a.saveOrgDoc(root, twoRoleDoc(), signNew); err != nil {
			t.Fatalf("saveOrgDoc: %v", err)
		}
		if got := len(callsWith(t, log, "--yes")); got != map[bool]int{false: 0, true: 1}[signNew] {
			t.Fatalf("signNew=%v: %d sign calls", signNew, got)
		}
	}
}

// Below monomind 2.21 the designer does no signature work, and asks the
// CLI about it once per few minutes, not on every save.
func TestSaveOrgDoc_NothingBelow221(t *testing.T) {
	log := fakeSignCLI(t, reconciledRead, statusUnsupported)
	a, root := newOrgDesignApp(t)
	signedDesignOrg(t, root)
	for i := 0; i < 2; i++ {
		d, err := orgdesign.Load(root, "growth")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.saveOrgDoc(root, d, false); err != nil {
			t.Fatalf("saveOrgDoc: %v", err)
		}
	}
	if n := len(callsWith(t, log, "--status")); n != 1 {
		t.Fatalf("--status asked %d times", n)
	}
	if n := len(callsWith(t, log, "--yes")); n != 0 {
		t.Fatalf("signed on 2.20: %d", n)
	}
}

// signNew=true (a new org signed as the user's own) is passed from
// CreateOrgDesign and nowhere else: SaveOrgDesign takes a whole document
// from the page, under any name.
func TestOnlyCreateOrgDesignSignsNewOrgs(t *testing.T) {
	fset := token.NewFileSet()
	var callers []string
	for _, name := range []string{"app_orgs_design.go", "app_orgs_design_save.go", "app_org_signature.go"} {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) == 0 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "saveAndRespondNew" && sel.Sel.Name != "saveOrgDoc") {
					return true
				}
				if id, ok := call.Args[len(call.Args)-1].(*ast.Ident); ok && id.Name == "true" {
					callers = append(callers, fn.Name.Name)
				}
				return true
			})
		}
	}
	sort.Strings(callers)
	if strings.Join(callers, ",") != "CreateOrgDesign" {
		t.Fatalf("signNew=true passed from %v, want CreateOrgDesign only", callers)
	}
}

func TestOrgSignArgs(t *testing.T) {
	if got := strings.Join(orgSignArgs("/p", "growth", "--status"), " "); got != "org --project /p sign growth --status" {
		t.Fatalf("args = %q", got)
	}
	if got := strings.Join(orgSignArgs("", "growth"), " "); got != "org sign growth" {
		t.Fatalf("args = %q", got)
	}
}
