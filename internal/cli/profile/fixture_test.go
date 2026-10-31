package profile

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/vault"
)

// The profile commands are tested here with the CLI around them faked through Deps. Most of
// them stay serial: profile folders and the vault resolve under $HOME, documents are indexed by
// the monomind found on $PATH, and printJSON writes to os.Stdout. The pure ones (capture view,
// folder choice, monomind projects) run in parallel.

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

// reportedError is an error the command already reported.
type reportedError struct{ error }

// errDeps is a Deps with only the CLI's errors, for the helpers that need no more.
var errDeps = Deps{NotFound: errorOf(2), InvalidInput: errorOf(3)}

func validateFolderChoice(dir string) error { return errDeps.validateFolderChoice(dir) }

// testDeps is the CLI around the commands for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and a name resolves to its id.
func testDeps(cfg *testConfig) Deps {
	d := errDeps
	d.JSONOutput = func() bool { return cfg.JSONOutput }
	d.ProfileID = func() string { return cfg.ProfileID }
	d.OpenDB = func() (*storage.Database, error) {
		db, err := storage.NewDatabase(cfg.DBPath)
		if err != nil {
			return nil, err
		}
		if err := db.ApplyMigrations(); err != nil {
			db.Close()
			return nil, err
		}
		if cfg.ProfileID == "" {
			// Test helper: a bare config acts on the seeded 'default' profile; the
			// real CLI has no such fallback (the profile gate refuses first).
			cfg.ProfileID = "default"
		} else {
			var id string
			err := db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ? OR name = ? LIMIT 1`, cfg.ProfileID, cfg.ProfileID).Scan(&id)
			if errors.Is(err, sql.ErrNoRows) {
				db.Close()
				return nil, errorOf(3)("profile %q not found (checked both id and name)", cfg.ProfileID)
			}
			if err == nil {
				cfg.ProfileID = id
			}
		}
		return db, nil
	}
	d.NewTable = func(w io.Writer, headers []string) *tablewriter.Table {
		table := tablewriter.NewWriter(w)
		table.Header(headers)
		return table
	}
	d.WithJSONErrors = func(*cobra.Command) {}
	d.Reported = func(err error) error { return reportedError{err} }
	return d
}

// newProfileDocsCLITestDB is a migrated database, with $HOME a temp dir so profile folders and
// the vault stay out of the real home, run as the user's own terminal.
func newProfileDocsCLITestDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	asOperator(t)
	// The vault syncs each file into the knowledge graph in the background;
	// let it finish before HOME is removed.
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "cli-profile-test.db")
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

// captureStdout returns what fn wrote to os.Stdout (the CLI's captureStdout).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	// Drain the pipe while fn runs: output larger than the pipe buffer (one page when
	// the runner's pipe quota is exhausted) would otherwise block the writer forever.
	type readResult struct {
		out []byte
		err error
	}
	drained := make(chan readResult, 1)
	go func() {
		b, err := io.ReadAll(r)
		drained <- readResult{b, err}
	}()
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	res := <-drained
	out, err := res.out, res.err
	if err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	return string(out)
}
