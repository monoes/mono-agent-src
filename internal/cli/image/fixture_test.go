package image

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
	"github.com/monoes/mono-agent/internal/vault"
)

// The image commands are tested here with the CLI around them faked through Deps. They stay
// serial: the vault and profile folders resolve under $HOME, which the tests set.

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

// exitCode is the exit code the CLI gives err: its own code, else 1.
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

// folderSyncResult is the --json shape of `image sync` (the CLI's folderSyncResult).
type folderSyncResult struct {
	ProfileID string   `json:"profile_id"`
	Root      string   `json:"root"`
	Scanned   int      `json:"scanned"`
	Added     int      `json:"added"`
	Updated   int      `json:"updated"`
	Removed   int      `json:"removed"`
	Changed   bool     `json:"changed"`
	Errors    []string `json:"errors"`
}

// testDeps is the CLI around the commands for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and an unknown one is exit 3.
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
			if cfg.ProfileID == "" {
				var id string
				_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
				if id == "" {
					id = "default"
				}
				cfg.ProfileID = id
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
		PrintFolderSync: func(cmd *cobra.Command, noun, profileID, root string, scanned int, r vault.ReconcileResult) error {
			res := folderSyncResult{ProfileID: profileID, Root: root, Scanned: scanned,
				Added: r.Added, Updated: r.Updated, Removed: r.Removed, Changed: r.Changed(), Errors: []string{}}
			for _, e := range r.Errs {
				res.Errors = append(res.Errors, e.Error())
			}
			if cfg.JSONOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scanned %d %s in %s\n", res.Scanned, noun, res.Root)
			return nil
		},
	}
}

func runFolderSync(t *testing.T, cfg *testConfig, args ...string) folderSyncResult {
	t.Helper()
	cmd := NewCommand(testDeps(cfg))
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v: %v (%s)", args, err, out.String())
	}
	var res folderSyncResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	return res
}

func syncCounts(r folderSyncResult) [3]int { return [3]int{r.Added, r.Updated, r.Removed} }

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seedProfiles(t *testing.T, dbPath string, ids ...string) {
	t.Helper()
	store, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, id := range ids {
		if _, err := store.DB.Exec(`INSERT INTO profiles (id, name, created_at) VALUES (?, ?, '2026-01-01')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
}

// newProfileDocsCLITestDB is a migrated database, with $HOME a temp dir so profile folders and
// the vault stay out of the real home.
func newProfileDocsCLITestDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(vault.Wait)
	dbPath := filepath.Join(t.TempDir(), "cli-image-test.db")
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
