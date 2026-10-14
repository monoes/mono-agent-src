package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/automations"
	"github.com/monoes/mono-agent/internal/action"
	"github.com/monoes/mono-agent/internal/automation"
	browserpkg "github.com/monoes/mono-agent/internal/browser"
	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/noderegistry"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testhome"
)

// The node commands are tested here with the CLI around them faked through Deps: a migrated
// database, the real node registry, no stored connections and a bridge that never connects.

// Tests never touch the real ~/.monoagent or the OS keychain; the automation packages come
// from the repo's automations/ source, as cmd/monoagentcli's TestMain does.
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

// testDeps runs the commands against the database at dbPath ("" for none that opens), in
// profile, with --json when jsonOut is set.
func testDeps(dbPath, profile string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		Verbose:    func() bool { return false },
		ProfileID:  func() string { return profile },
		OpenDB: func() (*storage.Database, error) {
			if dbPath == "" {
				return nil, errors.New("no database in this test")
			}
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
		BuildRegistry: noderegistry.Build,
		ResolveCredential: func(context.Context, *connections.Store, string, string) (map[string]interface{}, error) {
			return nil, errors.New("no connections in tests")
		},
		ConnectBridge: func(string, zerolog.Logger) (browserpkg.ExtensionBridge, error) {
			return nil, errors.New("no browser in tests")
		},
		ConfigDir:    func() string { return os.TempDir() },
		NotFound:     errCode(2),
		InvalidInput: errCode(3),
	}
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
