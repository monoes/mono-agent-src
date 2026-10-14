package hil

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The hil commands are tested here with the CLI around them faked through Deps. The send path
// (requireOperator, the runner, node run, publication) is tested end to end through the CLI's
// adapter in cmd/monoagentcli/hil_send_test.go; here RequireOperator refuses and SendRunner
// fails, so a test that reaches either fails loudly. The tests stay serial: they set HOME and
// the TypeSafe key, and the list prints to os.Stdout.

// Tests never touch the real ~/.monoagent or the OS keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	testhome.Main(m)
}

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

// exitCodeFor is the exit code the CLI gives err: its own code, else 1.
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

// testDeps is the CLI around the commands for cfg; the profile is cfg's, "" meaning "default".
func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(cfg.DBPath)
			if err != nil {
				return nil, err
			}
			if cfg.ProfileID == "" {
				cfg.ProfileID = "default"
			}
			return db, db.ApplyMigrations()
		},
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			table := tablewriter.NewWriter(w)
			table.Header(headers)
			return table
		},
		RequireOperator: func(what string) error {
			return errorOf(5)("fixture: only the operator can %s", what)
		},
		SendRunner: func() hilsend.Runner {
			return func(context.Context, hilsend.Run) (json.RawMessage, error) {
				return nil, errors.New("fixture: no sends in these tests")
			}
		},
		WorkspaceRoot: func(*sql.DB) (string, error) { return "", nil },
	}
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
