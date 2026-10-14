package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/agentinstall"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The agent commands are tested here with the CLI around them faked through Deps: a migrated
// database, fake exit-code errors, and plain streamed output.

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

// codeError is the fake of the CLI's errors that carry an exit code.
type codeError struct {
	code int
	msg  string
}

func (e *codeError) Error() string { return e.msg }

func errCode(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &codeError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// exitCodeFor is the code main would exit with for err.
func exitCodeFor(err error) int {
	var ce *codeError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	}
	return 1
}

// testDeps runs the commands against the database at dbPath, with --json when jsonOut is set.
func testDeps(dbPath string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			if err := db.ApplyMigrations(); err != nil {
				db.Close()
				return nil, err
			}
			return db, nil
		},
		NotFound:     errCode(2),
		InvalidInput: errCode(3),
		NewTable: func(w io.Writer, headers []string, align []tw.Align) *tablewriter.Table {
			table := tablewriter.NewTable(w,
				tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})))
			table.Header(headers)
			return table
		},
		ProgressWriter: func(w io.Writer, _ bool) func(string) {
			return func(line string) { fmt.Fprintf(w, "    %s\n", line) }
		},
		FinishStreamed: func(out io.Writer, _ bool, err error, okMsg string) error {
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ %s\n", okMsg)
			return nil
		},
		StdinIsTerminal: func() bool { return false },
	}
}

// installRuntime is installRuntime with the fake errors.
func installRuntime(ctx context.Context, m runtimeMachine, id string, force bool, confirmScript func(agentinstall.Recipe) bool,
	progress func(string)) (string, error) {
	return testDeps("", false).installRuntime(ctx, m, id, force, confirmScript, progress)
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
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

// newTestDBPath is a migrated database in a temp dir.
func newTestDBPath(t *testing.T) string {
	t.Helper()
	dbPath := t.TempDir() + "/cli-agent-test.db"
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	if err := db.DB.Close(); err != nil {
		t.Fatalf("closing seed db: %v", err)
	}
	return dbPath
}

// decodeJSON decodes out into v.
func decodeJSON(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
}
