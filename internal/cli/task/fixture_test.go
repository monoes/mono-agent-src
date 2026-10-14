package task

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// The task board's commands are tested here with the CLI around them faked through Deps: each
// test has a database of its own, and the environment that says who the caller is
// (MONOAGENT_ACTOR, the agent-context markers) and whether stdin is a terminal are the test's
// own (setenv, atTerminal), not the process's, so a test that touches no other global runs in
// parallel. The tests that set the local time zone (setLocalZone) do not.

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

// exitCode is the exit code the CLI gives err: 2 not found, 3 invalid input, 1 any other error.
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

// reportedError is an error the command already printed as its --json document.
type reportedError struct{ error }

func (e reportedError) Unwrap() error { return e.error }

// withJSONErrors is the fake of the CLI's: with --json, a failing command prints
// {"error", "code", the error's own fields} on stdout.
func withJSONErrors(jsonOut bool, cmd *cobra.Command) {
	run := cmd.RunE
	if run == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := run(c, args)
		if err == nil || !jsonOut {
			return err
		}
		body := map[string]any{"error": err.Error()}
		switch exitCode(err) {
		case 2:
			body["code"] = "not_found"
		case 3:
			body["code"] = "invalid_input"
		}
		var fields interface{ JSONErrorFields() map[string]any }
		if errors.As(err, &fields) {
			for k, v := range fields.JSONErrorFields() {
				if k != "error" {
					body[k] = v
				}
			}
		}
		b, _ := json.Marshal(body)
		fmt.Fprintln(c.OutOrStdout(), string(b))
		return reportedError{err}
	}
}

// A test's environment and terminal, by top-level test: a test and its subtests share them.
var (
	envMu     sync.Mutex
	envs      = map[string]map[string]string{}
	terminals = map[string]bool{}
)

func rootName(t testing.TB) string {
	name, _, _ := strings.Cut(t.Name(), "/")
	return name
}

// setenv sets key in the test's environment until t ends, as t.Setenv does in the process's.
func setenv(t testing.TB, key, value string) {
	t.Helper()
	root := rootName(t)
	envMu.Lock()
	defer envMu.Unlock()
	if envs[root] == nil {
		envs[root] = map[string]string{}
	}
	old, had := envs[root][key]
	envs[root][key] = value
	t.Cleanup(func() {
		envMu.Lock()
		defer envMu.Unlock()
		if had {
			envs[root][key] = old
		} else {
			delete(envs[root], key)
		}
	})
}

func getenv(t testing.TB) func(string) string {
	root := rootName(t)
	return func(key string) string {
		envMu.Lock()
		defer envMu.Unlock()
		return envs[root][key]
	}
}

// atTerminal makes `task agents allow` believe it runs at a terminal (or not) for the test.
func atTerminal(t testing.TB, yes bool) {
	t.Helper()
	root := rootName(t)
	envMu.Lock()
	defer envMu.Unlock()
	old, had := terminals[root]
	terminals[root] = yes
	t.Cleanup(func() {
		envMu.Lock()
		defer envMu.Unlock()
		if had {
			terminals[root] = old
		} else {
			delete(terminals, root)
		}
	})
}

// testDeps is the CLI around the commands for the test t, the database at dbPath and the
// profile --profile names ("" for the active one). Opening the database resolves the profile as
// the CLI's does: "" is the active profile (else default), and a name is its profile's id.
func testDeps(t testing.TB, dbPath, profile string, jsonOut bool) Deps {
	root := rootName(t)
	var mu sync.Mutex
	return Deps{
		JSONOutput: func() bool { return jsonOut },
		ProfileID: func() string {
			mu.Lock()
			defer mu.Unlock()
			return profile
		},
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			if profile == "" {
				var id string
				_ = db.DB.QueryRow(`SELECT value FROM settings WHERE key = 'active_profile_id'`).Scan(&id)
				if id == "" {
					id = "default"
				}
				profile = id
				return db, nil
			}
			var id string
			if err := db.DB.QueryRow(`SELECT id FROM profiles WHERE id = ?`, profile).Scan(&id); err != nil {
				if err := db.DB.QueryRow(`SELECT id FROM profiles WHERE LOWER(name) = LOWER(?)`, profile).Scan(&id); err != nil {
					db.Close()
					return nil, errorOf(3)("profile %q not found (checked both id and name)", profile)
				}
			}
			profile = id
			return db, nil
		},
		NotFound:     errorOf(2),
		InvalidInput: errorOf(3),
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
		WithJSONErrors: func(cmd *cobra.Command) { withJSONErrors(jsonOut, cmd) },
		Getenv:         getenv(t),
		StdinIsTerminal: func() bool {
			envMu.Lock()
			defer envMu.Unlock()
			return terminals[root]
		},
	}
}

// newTaskTestDB is a migrated database of the test's own.
func newTaskTestDB(t *testing.T) string {
	t.Helper()
	return testdb.Path(t)
}

// runTask runs `task <args>` and returns what it printed.
func runTask(t *testing.T, dbPath, profile string, jsonOut bool, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runTaskIn(t, dbPath, profile, jsonOut, strings.NewReader(stdin), args...)
}

// runTaskIn is runTask with a standard input of any kind.
func runTaskIn(t *testing.T, dbPath, profile string, jsonOut bool, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCommand(testDeps(t, dbPath, profile, jsonOut))
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(stdin)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// td is the CLI around the helpers that need only its errors, and an empty environment.
var td = Deps{NotFound: errorOf(2), InvalidInput: errorOf(3), Getenv: func(string) string { return "" }}

// jsonErrorFields is an error that adds fields to the --json error document.
type jsonErrorFields interface{ JSONErrorFields() map[string]any }

// jsonErrorCode is the code the CLI's --json error document gives err by its exit code.
func jsonErrorCode(err error) string {
	switch exitCode(err) {
	case 2:
		return "not_found"
	case 3:
		return "invalid_input"
	}
	return ""
}

// unsetenv removes key from the test's environment until t ends.
func unsetenv(t testing.TB, key string) {
	t.Helper()
	setenv(t, key, "")
	root := rootName(t)
	envMu.Lock()
	defer envMu.Unlock()
	delete(envs[root], key)
}
