package workflowio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/cli/workflowbundle"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The commands are tested here with the CLI around them faked through Deps: the store is the
// hybrid one over a temp workflow dir and a temp database, and the automation registry is empty.
// `import` keeps its index under $HOME and `export` prints on os.Stdout, so most tests are serial.

type testConfig struct {
	DBPath     string
	WFDir      string
	JSONOutput bool
	ProfileID  string
}

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

// reported is the fake of the CLI's reportedError.
type reported struct{ error }

func (r reported) Unwrap() error { return r.error }

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

func newTestConfig(t *testing.T) *testConfig {
	t.Helper()
	return &testConfig{DBPath: filepath.Join(t.TempDir(), "w.db"), WFDir: t.TempDir(), JSONOutput: true, ProfileID: "default"}
}

func testDeps(t *testing.T, cfg *testConfig) Deps {
	reg, err := automation.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
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
			files, err := workflow.NewWorkflowFileStore(cfg.WFDir)
			if err != nil {
				t.Fatal(err)
			}
			return workflow.NewHybridWorkflowStore(files, workflow.NewSQLiteWorkflowStore(db.DB))
		},
		CreateWorkflow: func(ctx context.Context, store *workflow.HybridWorkflowStore, wf *workflow.Workflow, _ bool) error {
			if err := store.CreateWorkflow(ctx, wf); err != nil {
				return err
			}
			for i := range wf.Nodes {
				if err := wf.Nodes[i].MarshalConfig(); err != nil {
					return err
				}
			}
			if err := store.SaveWorkflowNodes(ctx, wf.ID, wf.Nodes); err != nil {
				return err
			}
			return store.SaveWorkflowConnections(ctx, wf.ID, wf.Connections)
		},
		NotFound:        errorOf(2),
		InvalidInput:    errorOf(3),
		Reported:        func(err error) error { return reported{err} },
		WithJSONErrors:  func(*cobra.Command) {},
		StdinIsTerminal: func() bool { return false },
		Bundle: workflowbundle.Deps{
			OpenRegistry:            func() (*automation.Registry, error) { return reg, nil },
			InvalidInput:            errorOf(3),
			ErrConfirmationRequired: errors.New("confirmation required"),
		},
	}
}

// run runs `workflow <args>` with stdin and returns what it printed on os.Stdout plus the
// command's own output (cmd.OutOrStdout) and stderr (cmd.ErrOrStderr).
func run(t *testing.T, d Deps, stdin string, args ...string) (stdout, cmdOut, cmdErr string, err error) {
	t.Helper()
	root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(d.NewImportCmd(), d.NewExportCmd(), d.NewConvertCmd(), d.NewMigrateCmd())
	var o, e bytes.Buffer
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
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
	err = root.Execute()
	os.Stdout = orig
	w.Close()
	b := <-drained
	return string(b), o.String(), e.String(), err
}

// n8nFixture is an n8n export from internal/n8nimport's test data.
func n8nFixture(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "n8nimport", "testdata", "n8n", name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
	return p
}
