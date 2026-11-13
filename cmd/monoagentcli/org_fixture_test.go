package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/monoes/mono-agent/internal/testhome"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

type orgCLIFixture struct {
	cfg        *globalConfig
	root       string
	outboundWF string
	plainWF    string
}

// newOrgCLIFixture isolates HOME (profile folders, workflow file store,
// daemon heartbeat), points monomind at the fake script, and seeds one org
// plus two workflows: one with an outbound node, one without.
func newOrgCLIFixture(t *testing.T) *orgCLIFixture {
	t.Helper()
	home := t.TempDir()
	testhome.SetHome(t, home)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(home, "hb.json"))
	t.Setenv("MONOAGENT_API_ADDR", "")
	fake, err := filepath.Abs(filepath.Join("..", "..", "internal", "monomind", "testdata", "fake-monomind.sh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOMIND_BIN", fake)

	dbPath := filepath.Join(home, "test.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	store := workflow.NewSQLiteWorkflowStore(db.DB)
	ctx := context.Background()
	mk := func(name string, nodeType string) string {
		wf := &workflow.Workflow{Name: name, ProfileID: "default"}
		if err := store.CreateWorkflow(ctx, wf); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveWorkflowNodes(ctx, wf.ID, []workflow.WorkflowNode{
			{ID: wf.ID + "-t", WorkflowID: wf.ID, Type: "trigger.manual", Name: "start"},
			{ID: wf.ID + "-n", WorkflowID: wf.ID, Type: nodeType, Name: "step"},
		}); err != nil {
			t.Fatal(err)
		}
		return wf.ID
	}
	f := &orgCLIFixture{
		cfg:        &globalConfig{DBPath: dbPath, ProfileID: "default"},
		root:       profiledir.Root(db.DB, "default"),
		outboundWF: mk("Publish post", "comm.slack"),
		plainWF:    mk("Summarize", "core.set"),
	}
	db.Close()

	doc := orgdesign.NewOrg("growth", "grow", orgdesign.NewOrgOptions{})
	doc.Roles = append(doc.Roles, orgdesign.Role{ID: "writer", Title: "Writer", Type: "specialist", ReportsTo: strPtrCLI("lead"), Responsibilities: []string{}})
	if _, err := orgdesign.Save(f.root, doc); err != nil {
		t.Fatal(err)
	}
	return f
}

func strPtrCLI(s string) *string { return &s }

func (f *orgCLIFixture) run(t *testing.T, args ...string) (map[string]interface{}, error) {
	t.Helper()
	cmd := newOrgCmd(f.cfg)
	cmd.SetArgs(args)
	var runErr error
	out := captureStdout(t, func() { runErr = cmd.Execute() })
	if runErr != nil {
		return nil, runErr
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("org %v printed non-JSON %q: %v", args, out, err)
	}
	return m, nil
}

func (f *orgCLIFixture) mustRun(t *testing.T, args ...string) map[string]interface{} {
	t.Helper()
	m, err := f.run(t, args...)
	if err != nil {
		t.Fatalf("org %v: %v", args, err)
	}
	return m
}

func (f *orgCLIFixture) load(t *testing.T) *orgdesign.Doc {
	t.Helper()
	d, err := orgdesign.Load(f.root, "growth")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// C-20: a granted workflow cannot be deleted by accident; --force revokes
// the grant and strips it from the org JSON first.
func TestWorkflowDeleteChecksOrgReferences(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "grant", "add", "growth", "--role", "writer", "--automation", "summarize")

	err := checkWorkflowOrgReferences(context.Background(), f.cfg, f.plainWF, false)
	if err == nil || exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "grant to growth:writer") {
		t.Fatalf("unforced delete of a granted workflow: err=%v code=%d", err, exitCodeFor(err))
	}
	if err := checkWorkflowOrgReferences(context.Background(), f.cfg, f.plainWF, true); err != nil {
		t.Fatal(err)
	}
	writer, _ := f.load(t).FindRole("writer")
	if len(writer.Automations) != 0 || len(writer.ToolProviders) != 0 {
		t.Fatalf("forced delete left the grant in the JSON: %+v", writer)
	}
	if err := checkWorkflowOrgReferences(context.Background(), f.cfg, f.plainWF, false); err != nil {
		t.Fatalf("no references left, still refused: %v", err)
	}
}

// #284: saving a granted workflow with a new outbound node raises the
// grant's stored tier and the org file's display copy; removing the node
// again lowers neither.
func TestWorkflowSaveRaisesGrantTier(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize")

	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	tiers := func() (stored, display string) {
		gs, err := orggrant.NewStore(db.DB).ListGrants(ctx, "default", "growth", "lead")
		if err != nil || len(gs) != 1 {
			t.Fatalf("grants = %v, %v", gs, err)
		}
		lead, _ := f.load(t).FindRole("lead")
		return gs[0].Automation().Tier, lead.FindGrantSpec("summarize").Tier
	}
	if s, d := tiers(); s != "consequential" || d != "consequential" {
		t.Fatalf("before: stored %s, display %s", s, d)
	}

	store := openWorkflowStore(db)
	wf, err := store.GetWorkflow(ctx, f.plainWF)
	if err != nil || wf == nil {
		t.Fatalf("workflow: %v", err)
	}
	plain := append([]workflow.WorkflowNode(nil), wf.Nodes...)
	withSlack := append(append([]workflow.WorkflowNode(nil), plain...),
		workflow.WorkflowNode{ID: f.plainWF + "-slack", WorkflowID: f.plainWF, Type: "comm.slack", Name: "notify"})
	if err := store.SaveWorkflowNodes(ctx, f.plainWF, withSlack); err != nil {
		t.Fatal(err)
	}
	if s, d := tiers(); s != "irreversible" || d != "irreversible" {
		t.Fatalf("after adding an outbound node: stored %s, display %s", s, d)
	}

	if err := store.SaveWorkflowNodes(ctx, f.plainWF, plain); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, "reconcile")
	if s, d := tiers(); s != "irreversible" || d != "irreversible" {
		t.Fatalf("after removing it: stored %s, display %s (want no downgrade)", s, d)
	}
}

// TestOrgAdapterWiring pins what main hands internal/cli/org (orgDeps):
// --profile through initDB, the exit-code errors, the already-printed error
// main's error printing knows, the capability probe and the serving guard.
func TestOrgAdapterWiring(t *testing.T) {
	f := newOrgCLIFixture(t)
	run := func(cfg *globalConfig, args ...string) (string, error) {
		cmd := newOrgCmd(cfg)
		cmd.SetArgs(args)
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		var err error
		out := captureStdout(t, func() { err = cmd.Execute() })
		return out, err
	}

	// --profile by name resolves through initDB to the profile's own folder.
	workRoot := t.TempDir()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO profiles (id, name, root_dir) VALUES ('p-work', 'Work', ?)`, workRoot); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.MkdirAll(orgdesign.OrgsDir(workRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orgdesign.OrgsDir(workRoot), "acme.json"), []byte(`{"name":"acme"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(&globalConfig{DBPath: f.cfg.DBPath, ProfileID: "Work"}, "summary", "--fast")
	if err != nil || !strings.Contains(out, `"name":"acme"`) || strings.Contains(out, `"name":"growth"`) {
		t.Fatalf("summary under --profile Work: %v %s", err, out)
	}
	if _, err := run(&globalConfig{DBPath: f.cfg.DBPath, ProfileID: "nosuch"}, "grant", "list", "growth"); exitCodeFor(err) != 3 {
		t.Fatalf("unknown --profile: %v", err)
	}

	// Exit codes: not found (2) and invalid input (3).
	if _, err := run(f.cfg, "sign", "nosuch"); exitCodeFor(err) != 2 {
		t.Fatalf("sign of an unknown org: exit %d, %v", exitCodeFor(err), err)
	}
	if _, err := run(f.cfg, "autonomy", "set", "growth", "--tier", "bad"); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "must be <class>=<tier>") {
		t.Fatalf("bad --tier: exit %d, %v", exitCodeFor(err), err)
	}

	// An invalid org: validate prints its report and returns main's
	// reportedError, so --json adds no second document.
	fakeValidatingMonomind(t, "growth")
	_, err = run(f.cfg, "validate", "growth")
	var rep reportedError
	if exitCodeFor(err) != 1 || !errors.As(err, &rep) {
		t.Fatalf("invalid org: exit %d, %T %v", exitCodeFor(err), err, err)
	}
	var stdout, stderr bytes.Buffer
	reportCommandError([]string{"--json", "org", "validate", "growth"}, err, &stdout, &stderr)
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "invalid") {
		t.Fatalf("reportedError: stdout %q stderr %q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	reportCommandError([]string{"--json", "org", "status"}, errors.New("boom"), &stdout, &stderr)
	if !strings.Contains(stdout.String(), `{"error":"boom"}`) {
		t.Fatalf("plain org error: stdout %q", stdout.String())
	}

	// The capability probe is main's (coder's) capabilityProbe.
	for m := monomind.AgentContextMarker(); m != ""; m = monomind.AgentContextMarker() {
		t.Setenv(m, "")
	}
	oldProbe := capabilityProbe
	capabilityProbe = func(*cobra.Command) (*monomind.CapabilitySet, error) { return monomind.NewCapabilitySet("9.0.0"), nil }
	t.Cleanup(func() { capabilityProbe = oldProbe })
	if _, err := run(f.cfg, "role", "set-access", "growth", "lead", "full", "--yes-i-understand"); err == nil || !strings.Contains(err.Error(), "org-role-full-access") {
		t.Fatalf("full access on a monomind without the capability: %v", err)
	}

	// `org serve` is a serving command (main's servingCommand sets PreRun).
	serve, _, err := newOrgCmd(f.cfg).Find([]string{"serve"})
	if err != nil || serve.PreRun == nil {
		t.Fatalf("org serve: %v, PreRun set %v", err, serve != nil && serve.PreRun != nil)
	}
}

// fakeValidatingMonomind is a monomind stand-in whose `org validate`
// rejects the org named reject (exit 1) and accepts every other one.
func fakeValidatingMonomind(t *testing.T, reject string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	script := `#!/bin/sh
if [ "$1" = "--version" ] && [ "$2" = "--json" ]; then
  echo '{"v":1,"version":"2.10.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'
  exit 0
fi
if [ "$1" = "org" ] && [ "$2" = "validate" ]; then
  if [ "$3" = "` + reject + `" ]; then echo "role writer reports to missing role lead" >&2; exit 1; fi
  echo "ok"; exit 0
fi
exit 0
`
	p := filepath.Join(t.TempDir(), "monomind")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOMIND_BIN", p)
}
