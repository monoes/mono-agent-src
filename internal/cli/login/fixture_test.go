package login

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The login commands are tested here with the CLI around them faked through Deps: a migrated
// database, no automation registry (or a booted one), and a bridge that never connects.

// Tests never touch the real ~/.monoagent or the OS keychain; a booted registry is seeded with
// the repo's automations/ source, as cmd/monoagentcli's TestMain does.
func TestMain(m *testing.M) {
	keyring.MockInit()
	automation.TestSeed = automations.FS()
	action.SetTestFallback(automations.FS())
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

// exitCode is the code main would exit with for err.
func exitCode(err error) int {
	var ce *codeError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	}
	return 1
}

// testDeps runs the commands against the database at dbPath, in profile ("" is none), with
// --json when jsonOut is set, and with no automation registry.
func testDeps(dbPath, profile string, jsonOut bool) Deps {
	return Deps{
		JSONOutput:     func() bool { return jsonOut },
		ProfileID:      func() string { return profile },
		BrowserProfile: func() string { return profile },
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
		OpenRegistry: func() (*automation.Registry, error) { return nil, nil },
		ConnectBridge: func(string) (browserpkg.ExtensionBridge, error) {
			return nil, errors.New("no browser in tests")
		},
		NotFound:       errCode(2),
		InvalidInput:   errCode(3),
		AuthConnection: errCode(4),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		NewTable: func(w io.Writer, headers []string, align []tw.Align) *tablewriter.Table {
			row := tw.CellConfig{Alignment: tw.CellAlignment{Global: tw.AlignLeft}}
			if len(align) > 0 {
				row.Alignment = tw.CellAlignment{PerColumn: align}
			}
			table := tablewriter.NewTable(w,
				tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})),
				tablewriter.WithConfig(tablewriter.Config{Row: row}))
			table.Header(headers)
			return table
		},
	}
}

// newTestDB is a migrated database in a temp dir; its path is the second value.
func newTestDB(t *testing.T) (*storage.Database, string) {
	t.Helper()
	dbPath := t.TempDir() + "/login-test.db"
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db, dbPath
}

// runCmd runs cmd with args and stdin and returns stdout, stderr (with the error appended,
// as main prints it) and the exit code.
func runCmd(cmd interface {
	SetArgs([]string)
	SetOut(io.Writer)
	SetErr(io.Writer)
	SetIn(io.Reader)
	Execute() error
}, stdin string, args ...string) (string, string, int) {
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	err := cmd.Execute()
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), exitCode(err)
}
