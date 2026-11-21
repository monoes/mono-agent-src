package workflowgenerate

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai/generate"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	refcli "github.com/monoes/mono-agent/internal/cli/ref"
	"github.com/monoes/mono-agent/internal/cli/workflowedit"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/nodes/control"
	httpnodes "github.com/monoes/mono-agent/internal/nodes/http"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The command is tested here with the CLI around it faked through Deps: the agent is a fake
// (never a model), the registry has the core and HTTP nodes, the store is the workflow files
// of a temp dir plus SQLite, and saving is workflowedit's SaveDocument, as in the CLI. The
// command prints on its own output and nothing reads $HOME, so the tests run in parallel.

// testConfig is the fake of the CLI's global flags: --db-path, --json and --profile.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
	Home       string // the workflow files live in Home/.monoagent/workflows
}

// persistTestEnv is a temp home and a config with its database outside it.
func persistTestEnv(t *testing.T) (home string, cfg *testConfig) {
	t.Helper()
	home = t.TempDir()
	return home, &testConfig{DBPath: filepath.Join(t.TempDir(), "wf.db"), JSONOutput: true, ProfileID: "default", Home: home}
}

func openTestDB(cfg *testConfig) (*storage.Database, error) {
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	if err := testdb.EnsureDefaultProfile(db.DB); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func testRegistry(*sql.DB) *workflow.NodeTypeRegistry {
	reg := workflow.NewNodeTypeRegistry()
	control.RegisterAll(reg)
	httpnodes.RegisterAll(reg)
	return reg
}

// testStore is the hybrid store of cfg: files under its home, rows in its database.
func testStore(cfg *testConfig) func(db *storage.Database) *workflow.HybridWorkflowStore {
	return func(db *storage.Database) *workflow.HybridWorkflowStore {
		files, err := workflow.NewWorkflowFileStore(filepath.Join(cfg.Home, ".monoagent", "workflows"))
		if err != nil {
			panic(err)
		}
		return workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
	}
}

// testDeps is the CLI around the command for cfg; exec and scan are the fake agent.
func testDeps(cfg *testConfig, exec func(ctx context.Context, runtime, model string) (generate.ExecFunc, error),
	scan func(context.Context) (*monomind.ScanResult, error)) Deps {
	save := workflowedit.Deps{
		OwnedWorkflow: func(ctx context.Context, store *workflow.HybridWorkflowStore, _ *sql.DB, _, id string) *workflow.Workflow {
			wf, err := store.GetWorkflow(ctx, id)
			if err != nil {
				return nil
			}
			return wf
		},
		NotFound: clicore.NotFound,
	}
	if exec == nil {
		exec = func(context.Context, string, string) (generate.ExecFunc, error) {
			return nil, errors.New("no agent in this test")
		}
	}
	if scan == nil {
		scan = func(context.Context) (*monomind.ScanResult, error) { return &monomind.ScanResult{}, nil }
	}
	return Deps{
		JSONOutput:    func() bool { return cfg.JSONOutput },
		ProfileID:     func() string { return cfg.ProfileID },
		OpenDB:        func() (*storage.Database, error) { return openTestDB(cfg) },
		NodeRegistry:  testRegistry,
		WorkflowStore: testStore(cfg),
		SaveDocument:  save.SaveDocument,
		FindNodeDoc: func(typ string) *refcli.NodeDoc {
			if typ == "http.request" {
				return &refcli.NodeDoc{Type: typ, Short: "Make an HTTP request", Outputs: "main"}
			}
			return nil
		},
		ExecFor: exec,
		Scan:    scan,
	}
}

// runGenerate runs `workflow generate <args>` and returns what it printed (on the command's
// own output, as the CLI's stdout).
func runGenerate(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	var out strings.Builder
	root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(NewCommand(d))
	root.SetOut(&out)
	root.SetErr(io.Discard)
	root.SetArgs(append([]string{"generate"}, args...))
	err := root.Execute()
	return out.String(), err
}
