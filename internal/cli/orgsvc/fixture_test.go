package orgsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/org"
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

type testConfig struct{ DBPath string }

type orgCLIFixture struct {
	cfg        *testConfig
	root       string
	outboundWF string
	plainWF    string
}

// newOrgCLIFixture is cmd/monoagentcli's org fixture: it isolates HOME,
// points monomind at the fake script, and seeds one org plus two
// workflows, one with an outbound node and one without.
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
		cfg:        &testConfig{DBPath: dbPath},
		root:       profiledir.Root(db.DB, "default"),
		outboundWF: mk("Publish post", "comm.slack"),
		plainWF:    mk("Summarize", "core.set"),
	}
	db.Close()

	doc := orgdesign.NewOrg("growth", "grow", orgdesign.NewOrgOptions{})
	lead := "lead"
	doc.Roles = append(doc.Roles, orgdesign.Role{ID: "writer", Title: "Writer", Type: "specialist", ReportsTo: &lead, Responsibilities: []string{}})
	if _, err := orgdesign.Save(f.root, doc); err != nil {
		t.Fatal(err)
	}
	return f
}

// orgCmd is `org` on the fixture's database, profile default.
func (f *orgCLIFixture) orgCmd() *cobra.Command {
	fail := func(format string, a ...interface{}) error { return fmt.Errorf(format, a...) }
	return org.NewCommand(org.Deps{
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(f.cfg.DBPath)
			if err != nil {
				return nil, err
			}
			return db, db.ApplyMigrations()
		},
		ProfileID:         func() string { return "default" },
		OpenWorkflowStore: openTestWorkflowStore,
		JSONOutput:        func() bool { return false },
		NotFound:          fail,
		InvalidInput:      fail,
		Reported:          func(err error) error { return err },
		StdinIsTerminal:   func() bool { return false },
		Serving:           func(c *cobra.Command, _ ...func(*cobra.Command) bool) *cobra.Command { return c },
		CapabilityProbe: func(cmd *cobra.Command) (*monomind.CapabilitySet, error) {
			return monomind.Capabilities(cmd.Context())
		},
	})
}

func (f *orgCLIFixture) mustRun(t *testing.T, args ...string) map[string]interface{} {
	t.Helper()
	cmd := f.orgCmd()
	cmd.SetArgs(args)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	// Drain the pipe while the command runs: output larger than the pipe buffer
	// would otherwise block the writer forever.
	drained := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		drained <- b
	}()
	os.Stdout = w
	runErr := cmd.Execute()
	os.Stdout = orig
	w.Close()
	out := <-drained
	if runErr != nil {
		t.Fatalf("org %v: %v", args, runErr)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &m); err != nil {
		t.Fatalf("org %v printed non-JSON %q: %v", args, out, err)
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
