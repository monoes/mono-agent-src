package connect

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/connections"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/resources"
	"github.com/monoes/mono-agent/internal/storage"
)

// The connect commands are tested here with the CLI around them faked through Deps. The tests
// that save connections stay serial: the vault's key store (the go-keyring mock) is the
// process's.

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
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ce):
		return ce.code
	default:
		return 1
	}
}

// testEnv is a migrated database with the profiles "default" and "work".
type testEnv struct {
	dbPath string
	db     *storage.Database
}

func newSessionsTestEnv(t *testing.T) *testEnv {
	t.Helper()
	asTheOperator(t)
	keyring.MockInit()
	dbPath := filepath.Join(t.TempDir(), "monoagent.db")
	db := openDB(t, dbPath)
	if _, err := db.DB.Exec(`INSERT OR IGNORE INTO profiles (id, name, created_at) VALUES ('default', 'Default', '2026-01-01'), ('work', 'Work', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	return &testEnv{dbPath: dbPath, db: db}
}

func openDB(t *testing.T, dbPath string) *storage.Database {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	return db
}

// deps runs the commands against the env's database, in profile, with --json when jsonOut is
// set.
func (e *testEnv) deps(profile string, jsonOut bool) Deps {
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID:  func() string { return profile },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(e.dbPath)
			if err != nil {
				return nil, err
			}
			return db, db.ApplyMigrations()
		},
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		AuthFailed:   errorOf(4),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		ReadSecretValue: func(r io.Reader) (string, error) {
			b, err := io.ReadAll(r)
			if err != nil || len(b) == 0 {
				return "", fmt.Errorf("reading value from stdin: no value provided")
			}
			line, _, _ := strings.Cut(string(b), "\n")
			return strings.TrimRight(line, "\r"), nil
		},
		ScrubSecrets: func(err error, secrets ...string) error {
			msg := err.Error()
			for _, s := range secrets {
				if len(s) >= 6 {
					msg = strings.ReplaceAll(msg, s, "[redacted]")
				}
			}
			return errors.New(msg)
		},
		ConnectionSecrets: func(c *connections.Connection) []string {
			var out []string
			for _, v := range c.Data {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
			return out
		},
		ResourcesClient: resources.New,
	}
}

// run executes `monoagentcli [--profile p] [--json] connect <args>` with stdin and returns
// stdout, stderr and the exit code.
func (e *testEnv) run(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	profile, jsonOut := "", false
	for len(args) > 0 && args[0] != "connect" {
		switch args[0] {
		case "--profile":
			profile, args = args[1], args[2:]
		case "--json":
			jsonOut, args = true, args[1:]
		default:
			t.Fatalf("unexpected global argument %q", args[0])
		}
	}
	cmd := NewCommand(e.deps(profile, jsonOut))
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args[1:])
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err != nil {
		errOut.WriteString(err.Error())
	}
	return out.String(), errOut.String(), exitCode(err)
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
