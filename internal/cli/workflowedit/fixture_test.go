package workflowedit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The commands are tested here with the CLI around them faked through Deps: the store is SQLite
// alone (no workflow files under $HOME). Tests that read what a command prints on os.Stdout stay
// serial; the others run in parallel.

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

// testDeps is the CLI around the commands for cfg.
func testDeps(cfg *testConfig) Deps {
	return Deps{
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
		WorkflowStore: func(db *storage.Database) *workflow.HybridWorkflowStore {
			return workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(db.DB))
		},
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
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
	}
}

// execute runs `workflow <args>` with stdin.
func execute(cfg *testConfig, stdin string, args ...string) error {
	d := testDeps(cfg)
	root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(d.NewNodeCmd(), d.NewConnectCmd(), d.NewDisconnectCmd(), d.NewSaveCmd())
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	return root.Execute()
}

// run is execute plus what the command printed on os.Stdout.
func run(t *testing.T, cfg *testConfig, stdin string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = execute(cfg, stdin, args...) })
	return out, err
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

// load reads workflow id back from cfg's database.
func load(t *testing.T, cfg *testConfig, id string) *workflow.Workflow {
	t.Helper()
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wf, err := workflow.NewSQLiteWorkflowStore(db.DB).GetWorkflow(context.Background(), id)
	if err != nil || wf == nil {
		t.Fatalf("workflow %s: %v", id, err)
	}
	return wf
}
