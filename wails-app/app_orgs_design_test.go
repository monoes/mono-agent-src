package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/orgdesign"
)

// newOrgDesignApp returns a test App whose active profile's root is an empty
// temp directory, so org config files and the CLI's --project agree.
func newOrgDesignApp(t *testing.T) (*App, string) {
	t.Helper()
	a := newTestApp(t)
	root := filepath.Join(t.TempDir(), "profile-root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir profile root: %v", err)
	}
	if _, err := a.db.Exec("INSERT INTO profiles (id, name, root_dir) VALUES ('gui', 'GUI', ?)", root); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	a.selectForTest("gui")
	a.ctx = context.Background()
	return a, root
}

// fakeOrgCLI installs a stub monoagentcli on MONOAGENTCLI_BIN: `org
// validate` answers with validatePayload (exit 0, as the real command does —
// it reports invalidity in the payload, not the exit code), and `org
// reconcile-doc` saves its stdin and answers with reconcilePayload. Every
// call's argv is appended to the returned log.
func fakeOrgCLI(t *testing.T, validatePayload, reconcilePayload string) (argsLog, stdinFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub CLI is a shell script (unix-only)")
	}
	dir := t.TempDir()
	argsLog = filepath.Join(dir, "args.log")
	stdinFile = filepath.Join(dir, "stdin.json")
	script := "#!/bin/sh\necho \"$*\" >> '" + argsLog + "'\ncase \"$*\" in\n" +
		"  *reconcile-doc*) cat > '" + stdinFile + "'; printf '%s\\n' '" + reconcilePayload + "' ;;\n" +
		"  *validate*) printf '%s\\n' '" + validatePayload + "' ;;\n" +
		"  *) printf '{\"ok\":true}\\n' ;;\nesac\n"
	bin := filepath.Join(dir, fakeCLIName)
	if err := writeFakeCLI(bin, script); err != nil {
		t.Fatalf("write stub cli: %v", err)
	}
	t.Setenv("MONOAGENTCLI_BIN", bin)
	return argsLog, stdinFile
}

func strPtr(s string) *string { return &s }

// twoRoleDoc is a lead + worker org where worker holds one automation grant's
// display copy.
func twoRoleDoc() *orgdesign.Doc {
	return &orgdesign.Doc{
		Name: "growth", Goal: "grow", Status: "stopped",
		Roles: []orgdesign.Role{
			{ID: "lead", Title: "Lead", Type: "boss", ReportsTo: nil, Responsibilities: []string{"lead"}},
			{
				ID: "worker", Title: "Worker", ReportsTo: strPtr("lead"), Responsibilities: []string{"work"},
				Automations: []orgdesign.GrantSpec{{Alias: "publish_post"}},
			},
		},
		Automations: []orgdesign.AutomationRef{{WorkflowID: "wf-1", Alias: "publish_post"}},
	}
}

// TestSaveOrgDoc_RejectedSaveNeverReconciles covers the ordering bug:
// reconcile revokes the grant rows a role no longer backs, so it may not run
// until `monoagentcli org validate` has accepted the document — the rollback
// can only restore the FILE, never a revoked row.
func TestSaveOrgDoc_RejectedSaveNeverReconciles(t *testing.T) {
	log, _ := fakeOrgCLI(t, `{"v":1,"org":"growth","valid":false,"error":"monomind says no"}`, `{}`)
	a, root := newOrgDesignApp(t)

	if _, err := orgdesign.Save(root, twoRoleDoc()); err != nil {
		t.Fatalf("seed org file: %v", err)
	}
	// Drop the worker role — the change monomind's validate will reject.
	next := twoRoleDoc()
	next.Roles = next.Roles[:1]
	if _, err := a.saveOrgDoc(root, next, false); err == nil || !strings.Contains(err.Error(), "monomind says no") {
		t.Fatalf("saveOrgDoc = %v, want the CLI validate rejection", err)
	}
	for _, call := range loggedArgs(t, log) {
		if strings.Contains(call, "reconcile-doc") {
			t.Fatalf("a rejected save reached the grant rows: %q", call)
		}
	}
	// And the file must still be the pre-image, worker and all.
	back, err := orgdesign.Load(root, "growth")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(back.Roles) != 2 {
		t.Fatalf("rolled-back file has %d roles, want 2", len(back.Roles))
	}
}

// TestSaveOrgDoc_ReconcilesThroughTheCLI: an accepted save of a new org
// sends the document to `org reconcile-doc --new` and writes back the
// document the CLI returns (the rows' version of it).
func TestSaveOrgDoc_ReconcilesThroughTheCLI(t *testing.T) {
	reconciled := `{"v":1,"org":{"name":"growth","goal":"reconciled","status":"stopped","schedule":null,"roles":[{"id":"lead","title":"Lead","type":"boss","reports_to":null,"responsibilities":["lead"]}]},"reconcile":[]}`
	log, stdin := fakeOrgCLI(t, `{"v":1,"org":"growth","valid":true}`, reconciled)
	a, root := newOrgDesignApp(t)

	d := twoRoleDoc()
	if _, err := a.saveOrgDoc(root, d, false); err != nil {
		t.Fatalf("saveOrgDoc: %v", err)
	}
	calls := loggedArgs(t, log)
	want := "--profile gui --json org reconcile-doc growth --new --by gui"
	found := false
	for _, c := range calls {
		found = found || c == want
	}
	if !found {
		t.Fatalf("CLI calls = %q, want %q", calls, want)
	}
	sent, err := os.ReadFile(stdin)
	if err != nil || !strings.Contains(string(sent), `"publish_post"`) {
		t.Fatalf("reconcile-doc stdin = %s, %v", sent, err)
	}
	back, err := orgdesign.Load(root, "growth")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if back.Goal != "reconciled" || len(back.Roles) != 1 || d.Goal != "reconciled" {
		t.Fatalf("saved %+v (caller's doc goal %q), want the reconciled document", back, d.Goal)
	}
}
