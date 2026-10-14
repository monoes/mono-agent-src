package summarycli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/extension"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The summary command is tested here with the CLI around it faked through Deps. The tests stay
// serial: they point HOME, the daemon heartbeat and the extension port at throwaway values,
// and the command prints to os.Stdout.

// Tests never touch the real ~/.monoagent or the OS keychain; the official automation packages
// come from the repo's automations/ source, as in the CLI's tests.
func TestMain(m *testing.M) {
	keyring.MockInit()
	automation.TestSeed = automations.FS()
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

// testDeps is the CLI around the command for cfg. OpenDB resolves the profile as the CLI's
// initDB does: "" is the active profile (else "default"), and a name resolves to its id.
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
					return nil, &cliError{code: 3, msg: fmt.Sprintf("profile %q not found (checked both id and name)", cfg.ProfileID)}
				}
				if err == nil {
					cfg.ProfileID = id
				}
			}
			return db, nil
		},
		InvalidInput: func(format string, a ...interface{}) error {
			return &cliError{code: 3, msg: fmt.Sprintf(format, a...)}
		},
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		// The CLI's extensionProbeAddrs: the tests set the port env to one nothing answers on.
		ProbeAddrs: func() []string {
			if p := strings.TrimSpace(os.Getenv(extension.ExtensionPortEnv)); p != "" {
				return []string{"http://127.0.0.1:" + p}
			}
			return []string{"http://127.0.0.1:" + extension.DefaultExtensionPort, "http://127.0.0.1:" + extension.FallbackExtensionPort}
		},
		OpenAutomationRegistry: func() (*automation.Registry, error) {
			reg, err := automation.Default()
			if err != nil {
				return nil, err
			}
			if _, err := reg.Boot(); err != nil {
				return nil, err
			}
			return reg, nil
		},
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
