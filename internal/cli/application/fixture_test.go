package application

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/olekukonko/tablewriter/renderer"
	"github.com/olekukonko/tablewriter/tw"

	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
	"github.com/monoes/mono-agent/internal/vault"
)

// `application`: the store is internal/applications' (tested there); these tests are about the
// commands. The CLI around them is faked through Deps, and a test that touches no package-level
// seam (internal/matching's, documents.RenderPDFFunc, apply.OpenForApplicationFunc) and writes
// no document has a database of its own and runs in parallel.

// cliError is the fake of the CLI's exit-code errors.
type cliError struct {
	code int
	msg  string
}

func (e *cliError) Error() string { return e.msg }

// code is the exit code the CLI gives err: 2 not found, 3 invalid input, 1 any other error.
func code(err error) int {
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

func errorOf(code int) func(format string, a ...interface{}) error {
	return func(format string, a ...interface{}) error {
		return &cliError{code: code, msg: fmt.Sprintf(format, a...)}
	}
}

// testDeps is the CLI around the commands for the database at dbPath and the default profile:
// no extension bridge, and a prompt that says no.
func testDeps(dbPath string, jsonOut bool) Deps {
	return Deps{
		JSONOutput:   func() bool { return jsonOut },
		ProfileID:    func() string { return "default" },
		OpenDB:       func() (*storage.Database, error) { return storage.NewDatabase(dbPath) },
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			cellFmt := tw.CellFormatting{AutoWrap: tw.WrapNone}
			table := tablewriter.NewTable(w,
				tablewriter.WithRenderer(renderer.NewBlueprint(tw.Rendition{Borders: tw.BorderNone})),
				tablewriter.WithConfig(tablewriter.Config{
					Header: tw.CellConfig{Formatting: cellFmt},
					Row:    tw.CellConfig{Formatting: cellFmt, Alignment: tw.CellAlignment{Global: tw.AlignLeft}},
				}),
			)
			table.Header(headers)
			return table
		},
		Bridge:  func(string) (browserpkg.ExtensionBridge, error) { return nil, errors.New("no extension in tests") },
		Confirm: func(string) bool { return false },
	}
}

// run runs `application <args>` on the database at dbPath and returns its stdout.
func run(t *testing.T, dbPath string, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	return runWith(t, testDeps(dbPath, jsonOut), args...)
}

func runWith(t *testing.T, d Deps, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(d)
	cmd.SetArgs(args)
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	err := cmd.Execute()
	return out.String(), err
}

// homeDB is a database for a test that writes documents: `apply` registers them in the vault,
// which falls back to $HOME, so HOME is a temporary directory and the test is not parallel.
func homeDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	// The vault syncs each file into the knowledge graph in the background;
	// let it finish before HOME is removed.
	t.Cleanup(vault.Wait)
	return testdb.Path(t)
}
