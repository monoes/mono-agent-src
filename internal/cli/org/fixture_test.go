package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"
	"golang.org/x/term"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/profiledir"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TestMain does what cmd/monoagentcli's does: it keeps the tests out of the
// real home, mocks the keychain, and seeds the official packages from the
// repo's automations/ source.
func TestMain(m *testing.M) {
	keyring.MockInit()
	automation.TestSeed = automations.FS()
	action.SetTestFallback(automations.FS())
	testhome.Main(m)
}

// testConfig is the part of the CLI's config the org commands read.
type testConfig struct {
	DBPath     string
	ProfileID  string
	JSONOutput bool
}

// cliError is the fake of the CLI's exit-code error: 2 not found, 3
// invalid input.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

// reportedError is the fake of the CLI's already-printed error.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// exitCodeFor is the exit code the CLI gives err.
func exitCodeFor(err error) int {
	var ce *cliError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	default:
		return 1
	}
}

// testDeps is Deps on cfg the way the CLI wires it: the database at
// cfg.DBPath, migrated, with --profile resolved as initDB does it ("" is
// the active profile, else default; a name resolves to its id).
func testDeps(cfg *testConfig) Deps {
	return Deps{
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			if cfg.ProfileID == "" {
				// Test helper: a bare config acts on the seeded 'default' profile; the
				// real CLI has no such fallback (the profile gate refuses first).
				cfg.ProfileID = "default"
				return db, nil
			}
			var id string
			if err := db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ? OR name = ? ORDER BY id = ? DESC LIMIT 1`, cfg.ProfileID, cfg.ProfileID, cfg.ProfileID).Scan(&id); err != nil {
				db.Close()
				return nil, &cliError{code: 3, msg: fmt.Sprintf("profile %q not found (checked both id and name)", cfg.ProfileID)}
			}
			cfg.ProfileID = id
			return db, nil
		},
		ProfileID:         func() string { return cfg.ProfileID },
		OpenWorkflowStore: openTestWorkflowStore,
		JSONOutput:        func() bool { return cfg.JSONOutput },
		NotFound: func(format string, a ...interface{}) error {
			return &cliError{code: 2, msg: fmt.Sprintf(format, a...)}
		},
		InvalidInput: func(format string, a ...interface{}) error {
			return &cliError{code: 3, msg: fmt.Sprintf(format, a...)}
		},
		Reported:        func(err error) error { return reportedError{err} },
		StdinIsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		Serving:         func(c *cobra.Command, _ ...func(*cobra.Command) bool) *cobra.Command { return c },
		CapabilityProbe: func(cmd *cobra.Command) (*monomind.CapabilitySet, error) {
			return monomind.Capabilities(cmd.Context())
		},
	}
}

// newOrgCmd is `org` on cfg.
func newOrgCmd(cfg *testConfig) *cobra.Command { return NewCommand(testDeps(cfg)) }

// testEnv is the org environment of `org` on cfg, without --project.
func testEnv(cfg *testConfig) *orgEnv {
	d := testDeps(cfg)
	return newEnv(d, orgenvFor(d, func() string { return "" }))
}

// depsEnv is an environment for helpers that only build errors.
func depsEnv() *orgEnv { return newEnv(testDeps(&testConfig{}), nil) }

// openTestWorkflowStore is the CLI's workflow store: files under
// ~/.monoagent/workflows plus SQLite.
func openTestWorkflowStore(db *storage.Database) *workflow.HybridWorkflowStore {
	home, _ := os.UserHomeDir()
	files, err := workflow.NewWorkflowFileStore(filepath.Join(home, ".monoagent", "workflows"))
	if err != nil {
		return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
	}
	return workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
}

// captureStdout redirects os.Stdout while fn runs and returns what was
// written: the org commands print with fmt.Println.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	// A file, not a pipe: a pipe needs a concurrent reader (output larger than
	// the pipe buffer would block fn), and a reader goroutine blocked on a real
	// descriptor stops the fake clock of the synctest bubbles that call this.
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatalf("os.CreateTemp: %v", err)
	}
	defer f.Close()
	orig := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = orig }()
	fn()
	out, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

type orgCLIFixture struct {
	cfg        *testConfig
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
	t.Setenv("HOME", home)
	t.Setenv("MONOAGENT_DAEMON_HEARTBEAT", filepath.Join(home, "hb.json"))
	t.Setenv("MONOAGENT_API_ADDR", "")
	fake, err := filepath.Abs(filepath.Join("..", "..", "monomind", "testdata", "fake-monomind.sh"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MONOMIND_BIN", fake)
	asOrgOperator(t) // a test from an agent's shell inherits its markers

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
		cfg:        &testConfig{DBPath: dbPath, ProfileID: "default"},
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

// staleProvider points the writer's provider somewhere old, the way a moved
// or hand-edited org file looks before reconcile regenerates it (as in
// cmd/monoagentcli's daemon_org_watch_test.go).
func staleProvider(t *testing.T, root string) {
	t.Helper()
	d, err := orgdesign.LoadPath(filepath.Join(orgdesign.OrgsDir(root), "growth.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := d.FindRole("writer")
	r.ToolProviders[0].Command = "/old/place/monoagentcli"
	if _, err := orgdesign.Save(root, d); err != nil {
		t.Fatal(err)
	}
}

// providerCommand is the command of writer's first tool provider in
// growth (daemon_org_watch_test.go's helper in cmd/monoagentcli).
func providerCommand(t *testing.T, root string) string {
	t.Helper()
	d, err := orgdesign.LoadPath(filepath.Join(orgdesign.OrgsDir(root), "growth.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, _ := d.FindRole("writer")
	return r.ToolProviders[0].Command
}

// exitCodeOf is the code of err, which must carry one.
func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v carries no exit code", err)
	}
	return ce.code
}
