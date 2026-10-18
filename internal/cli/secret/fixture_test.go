package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/storage"
)

// The secret commands are tested here with the CLI around them faked through Deps. They stay
// serial: the vault's key store (the go-keyring mock), HOME, os.Stdin and the working
// directory they use are the process's.

// notFound is the fake of the CLI's not-found error (exit code 2).
type notFound struct{ msg string }

func (e *notFound) Error() string { return e.msg }

// testDeps runs the commands against the database at dbPath, in profile,
// with --json when jsonOut is set.
func testDeps(dbPath, profile string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID:  func() string { return profile },
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
		NotFound: func(format string, a ...interface{}) error {
			return &notFound{msg: fmt.Sprintf(format, a...)}
		},
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
	}
}

func newSecretCLITestDB(t *testing.T) string {
	t.Helper()
	asTheOperator(t)
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "cli-secret-test.db")
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

// runSecretCmd runs `secret <args>` as --json and returns its stdout.
func runSecretCmd(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	cmd := NewCommand(testDeps(dbPath, "p1", true))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	err := cmd.Execute()
	return out.String(), err
}

// asTheOperator clears what makes this process not the operator's own
// terminal: a test started from an agent's shell inherits its markers, and
// the commands that print secrets refuse those (internal/cli/revealgate).
func asTheOperator(t *testing.T) {
	t.Helper()
	for _, k := range operator.MarkerEnv() {
		t.Setenv(k, "")
	}
}
