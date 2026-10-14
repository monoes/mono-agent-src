package workflowmanage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The commands are tested here with the CLI around them faked through Deps: the store is SQLite
// alone (no workflow files under $HOME) and there is no engine. Tests that read what a command
// prints on os.Stdout stay serial; the others run in parallel.

// testConfig is the fake of the CLI's global flags: --db-path, --json and --profile.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
}

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

func errorOf(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// exitCode is the exit code the CLI gives err.
func exitCode(err error) int {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce.code
	}
	if err != nil {
		return 1
	}
	return 0
}

// newTestDB is a migrated database in a temp dir, seeded with sqlText.
func newTestDB(t *testing.T, sqlText string) *testConfig {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "w.db")
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	if sqlText != "" {
		if _, err := db.DB.Exec(sqlText); err != nil {
			t.Fatal(err)
		}
	}
	return &testConfig{DBPath: dbPath, JSONOutput: true, ProfileID: "default"}
}

// twoProfileDB holds workflow w1 (default) and w2 (Work), with runs in both profiles.
func twoProfileDB(t *testing.T) *testConfig {
	return newTestDB(t, `INSERT INTO profiles (id, name) VALUES ('p-work', 'Work');
		INSERT INTO workflows (id, name, is_active, profile_id) VALUES ('w1','A',1,'default'),('w2','B',1,'p-work');
		INSERT INTO workflow_executions (id, workflow_id, status, profile_id, created_at) VALUES
		  ('e-old','w1','SUCCESS','default','2026-09-27 10:00:00'),
		  ('e-new','w2','FAILED','p-work','2026-09-27 11:00:00'),
		  ('e-mid','w1','SUCCESS','default','2026-09-27 10:30:00')`)
}

func sqliteStore(db *storage.Database) *workflow.HybridWorkflowStore {
	return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
}

// testDeps is the CLI around the commands for cfg.
func testDeps(cfg *testConfig) Deps {
	d := Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		WorkflowStore: sqliteStore,
		OwnedWorkflow: func(ctx context.Context, store *workflow.HybridWorkflowStore, db *sql.DB, profileID, id string) *workflow.Workflow {
			wf, err := store.GetWorkflow(ctx, id)
			if err != nil || wf == nil {
				return nil
			}
			var owner string
			_ = db.QueryRowContext(ctx, `SELECT profile_id FROM workflows WHERE id = ?`, id).Scan(&owner)
			if owner != "" && profileID != "" && owner != profileID {
				return nil
			}
			return wf
		},
		CreateWorkflow: func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, _ bool) error {
			if err := store.CreateWorkflow(ctx, wf); err != nil {
				return err
			}
			if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
				return err
			}
			return store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections)
		},
		BuildEngine: func(bool) (*workflow.WorkflowEngine, func(), error) {
			return nil, func() {}, errors.New("no engine in tests")
		},
		CheckOrgReferences: func(context.Context, string, bool) error { return nil },
		NotFound:           errorOf(2),
		InvalidInput:       errorOf(3),
	}
	d.LoadWorkflow = func(ctx context.Context, id string) (*workflow.Workflow, error) {
		db, err := d.OpenDB()
		if err != nil {
			return nil, err
		}
		defer db.Close()
		wf, err := sqliteStore(db).GetWorkflow(ctx, id)
		if err != nil {
			return nil, err
		}
		if wf == nil {
			return nil, d.NotFound("workflow %q not found", id)
		}
		return wf, nil
	}
	return d
}

// run runs `workflow <args>` (the commands of this package under one parent) and returns what
// it printed on os.Stdout.
func run(t *testing.T, cfg *testConfig, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = execute(cfg, io.Discard, args...) })
	return out, err
}

// execute runs `workflow <args>` with the command's own output (cmd.OutOrStdout) on out.
func execute(cfg *testConfig, out io.Writer, args ...string) error {
	d := testDeps(cfg)
	root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(d.NewListCmd(), d.NewGetCmd(), d.NewCreateCmd(), d.NewActivateCmd(), d.NewDeactivateCmd(),
		d.NewDeleteCmd(), d.NewExecutionsCmd(), d.NewInputsCmd(), d.NewVersionsCmd())
	root.SetOut(out)
	root.SetArgs(args)
	return root.Execute()
}

// captureStdout returns what fn wrote to os.Stdout (the CLI's captureStdout).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}
