package capturecli

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/olekukonko/tablewriter"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The capture commands are tested here with the CLI around them faked through Deps. They stay
// serial: the inboxes resolve under $HOME and the capture env vars, which the tests set.

// Tests never touch the real ~/.monoagent, ~/.monomind or the OS keychain.
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

// exitCodeFor is the exit code the CLI gives err: 2 not found, 3 invalid input, 1 any other.
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

// testDeps is the CLI around the commands for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and a name resolves to its id.
func testDeps(cfg *testConfig) Deps {
	return Deps{
		JSONOutput: func() bool { return cfg.JSONOutput },
		ProfileID:  func() string { return cfg.ProfileID },
		DBPath:     func() string { return cfg.DBPath },
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
		NewTable: func(w io.Writer, headers []string) *tablewriter.Table {
			table := tablewriter.NewWriter(w)
			table.Header(headers)
			return table
		},
	}
}
