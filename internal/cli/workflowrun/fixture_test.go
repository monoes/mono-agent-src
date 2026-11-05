package workflowrun

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/nodes/control"
	"github.com/monoes/mono-agent/internal/nodes/evalnode"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The commands are tested here with the CLI around them faked through Deps: the engine is a
// real one over the test database with only the core nodes, no browser, no scheduler. The
// commands print on os.Stdout, so the tests that run them stay serial.

// testConfig is the fake of the CLI's global flags: --db-path, --json and --profile.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
}

// exitCode is the exit code the CLI gives err.
func exitCode(err error) int { return clicore.ExitCodeFor(err) }

func openTestDB(cfg *testConfig) (*storage.Database, error) {
	db, err := storage.NewDatabase(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	if err := db.ApplyMigrations(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func testRegistry() *workflow.NodeTypeRegistry {
	reg := workflow.NewNodeTypeRegistry()
	control.RegisterAll(reg)
	evalnode.RegisterAll(reg)
	// The bundled "hello" template starts with an http.request step; the tests have no network.
	reg.Register("http.request", func() workflow.NodeExecutor { return stubHTTPNode{} })
	return reg
}

// stubHTTPNode answers the template's request with a sample user.
type stubHTTPNode struct{}

func (stubHTTPNode) Type() string { return "http.request" }
func (stubHTTPNode) Execute(_ context.Context, _ workflow.NodeInput, _ map[string]interface{}) ([]workflow.NodeOutput, error) {
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(map[string]interface{}{"body": map[string]interface{}{
		"name": "n", "email": "e", "company": map[string]interface{}{"name": "c"}, "address": map[string]interface{}{"city": "x"}}})}}}, nil
}

// dryRuns records the workflow ids DryRun was called with.
var dryRuns []string

// testDeps is the CLI around the commands for cfg.
// testInTurn is the turn check of the commands under test: not in a turn unless a test says so.
var testInTurn = func() (bool, error) { return false, nil }

func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB:     func() (*storage.Database, error) { return openTestDB(cfg) },
		BuildEngine: func() (*workflow.WorkflowEngine, func(), error) {
			db, err := openTestDB(cfg)
			if err != nil {
				return nil, nil, err
			}
			engine := workflow.NewWorkflowEngineWithStore(workflow.NewSQLiteWorkflowStore(db.DB), db.DB, nil, testRegistry(),
				workflow.EngineConfig{ProfileID: cfg.ProfileID, WebhookAddr: "127.0.0.1:0", MaxConcurrent: 1}, zerolog.Nop())
			return engine, func() { db.Close() }, nil
		},
		NodeTypes: testRegistry,
		InTurn:    func() (bool, error) { return testInTurn() },
		DryRun: func(cmd *cobra.Command, workflowID string) error {
			dryRuns = append(dryRuns, workflowID)
			return errors.New("dry run")
		},
	}
}

// runWorkflowCmdIn runs `workflow <args>` (the commands of this package under one parent) with
// stdin, returning what it printed on os.Stdout and the command's error.
func runWorkflowCmdIn(t *testing.T, cfg *testConfig, stdin string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		d := testDeps(cfg)
		root := &cobra.Command{Use: "workflow", SilenceUsage: true, SilenceErrors: true}
		templates := &cobra.Command{Use: "templates"}
		templates.AddCommand(d.NewTemplatesRunCmd())
		root.AddCommand(d.NewRunCmd(), d.NewExecutionCmd(), d.NewCancelCmd(), d.NewRetryCmd(), templates)
		root.SetArgs(args)
		root.SetIn(strings.NewReader(stdin))
		err = root.Execute()
	})
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
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	return <-done
}
