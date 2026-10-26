package chat

import (
	"database/sql"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

// The commands are tested here with the CLI around them faked through Deps (the database is a
// real one, the agent a fake monomind). Most tests set HOME, point monomind at a fake binary
// or read what the commands print on os.Stdout, so they stay serial.

// testConfig is the fake of the CLI's global flags: --db-path, --json and --profile.
type testConfig struct {
	DBPath     string
	JSONOutput bool
	ProfileID  string
}

// testDeps is the CLI around the commands for c. OpenDB resolves an empty profile to
// "default", as the CLI's does when no profile is active.
func testDeps(c *testConfig) *Deps {
	return &Deps{
		JSONOutput: func() bool { return c.JSONOutput },
		ProfileID:  func() string { return c.ProfileID },
		DBPath:     func() string { return c.DBPath },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(clicore.ExpandPath(c.DBPath))
			if err != nil {
				return nil, err
			}
			if err := migrateOnce(db, clicore.ExpandPath(c.DBPath)); err != nil {
				db.Close()
				return nil, err
			}
			if c.ProfileID == "" {
				c.ProfileID = "default"
			}
			return db, nil
		},
		ResolveProfileID: func(db *sql.DB, idOrName string) (string, error) {
			var id string
			if err := db.QueryRow(`SELECT id FROM profiles WHERE id = ? OR LOWER(name) = LOWER(?)`, idOrName, idOrName).Scan(&id); err != nil {
				return "", clicore.InvalidInput("profile %q not found (checked both id and name)", idOrName)
			}
			return id, nil
		},
		RequireOperator:   func(string) error { return nil },
		PublicationPrompt: func() string { return "Register what you publish." },
	}
}

var migrated sync.Map // DB path -> struct{}: the paths migrated in this test binary

// migrateOnce migrates db the first time a path is opened. A command opens the
// database on every call, and re-checking an already migrated database costs
// ~50ms (more under -race) each time; the tests' databases are copies of the
// one migrated template, and nothing in these tests changes their schema.
func migrateOnce(db *storage.Database, path string) error {
	if _, done := migrated.Load(path); done {
		return nil
	}
	if err := db.ApplyMigrations(); err != nil {
		return err
	}
	migrated.Store(path, struct{}{})
	return nil
}

// exitCodeFor is the exit code the CLI gives err.
func exitCodeFor(err error) int { return clicore.ExitCodeFor(err) }

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
