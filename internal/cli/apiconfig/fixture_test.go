package apiconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/monoes/mono-agent/internal/apiconfig"
	"github.com/monoes/mono-agent/internal/autostart"
	"github.com/monoes/mono-agent/internal/daemonhb"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/testdb"
)

// `api config show|set|unset`: the documents are internal/apiconfig's (tested there); these tests
// are about the commands: flags, exit codes, what goes to stdout and stderr, and the text. Each
// test has a fixture of its own (a database, the API's variables, the daemon's heartbeat and the
// service manager), so the tests share nothing and run in parallel.

// invalidInput is the fake of the CLI's invalid-input error (exit code 3).
type invalidInput struct{ msg string }

func (e *invalidInput) Error() string { return e.msg }

// code is the exit code the CLI gives err: 3 for invalid input, 1 for any other error.
func code(err error) int {
	var in *invalidInput
	switch {
	case err == nil:
		return 0
	case errors.As(err, &in):
		return 3
	default:
		return 1
	}
}

func invalid(format string, a ...interface{}) error {
	return &invalidInput{msg: fmt.Sprintf(format, a...)}
}

// fixture is one test's database, environment, daemon heartbeat and service manager.
type fixture struct {
	db      string
	refusal string // what Deps.Refusal answers: an agent's shell when set

	mu        sync.Mutex
	env       map[string]string
	hb        daemonhb.Heartbeat
	live      bool
	installer autostart.Installer
}

// configTest is a database of its own with the API's variables clear, no daemon and a service
// manager that does nothing.
func configTest(t *testing.T) *fixture {
	t.Helper()
	return &fixture{db: testdb.Path(t), env: map[string]string{}, installer: &fakeAutostart{}}
}

func (f *fixture) setenv(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.env[key] = value
}

// beat makes hb the heartbeat of a live daemon.
func (f *fixture) beat(hb daemonhb.Heartbeat) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hb, f.live = hb, true
	return nil
}

func (f *fixture) useInstaller(in autostart.Installer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installer = in
}

func (f *fixture) deps(jsonOut bool) Deps {
	return Deps{
		Refusal:    func() string { return f.refusal },
		JSONOutput: func() bool { return jsonOut },
		OpenDB:     func() (*storage.Database, error) { return storage.NewDatabase(f.db) },
		Env: func() apiconfig.Env {
			f.mu.Lock()
			defer f.mu.Unlock()
			env := make(map[string]string, len(f.env))
			for k, v := range f.env {
				env[k] = v
			}
			hb, live, in := f.hb, f.live, f.installer
			return apiconfig.Env{
				Getenv:    func(k string) string { return env[k] },
				Heartbeat: func() (daemonhb.Heartbeat, bool) { return hb, live },
				Installer: in,
			}
		},
		InvalidInput: invalid,
		// As the CLI's asCLIError: a value that fails its check, and a damaged saved row, are invalid input.
		AsCLIError: func(err error) error {
			var input *apiconfig.InputError
			var bad *apiconfig.ValidationError
			if errors.As(err, &input) || errors.As(err, &bad) || errors.Is(err, apiconfig.ErrDamaged) {
				return invalid("%v", err)
			}
			return err
		},
		WriteJSON: func(w io.Writer, v any) error {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(v)
		},
	}
}

// run runs `api config <args>` and returns stdout and stderr separately.
func (f *fixture) run(t *testing.T, jsonOut bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := NewCommand(f.deps(jsonOut))
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// saveAt saves settings ("key=value") in the database at dbPath.
func saveAt(t *testing.T, dbPath string, words ...string) {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := apiconfig.Update(context.Background(), db.DB, func(s *apiconfig.Settings) error {
		for _, w := range words {
			key, value, _ := strings.Cut(w, "=")
			if err := s.Set(key, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func savedAt(t *testing.T, dbPath string) apiconfig.Settings {
	t.Helper()
	db, err := storage.NewDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := apiconfig.Load(context.Background(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decodeConfig(t *testing.T, out string) apiconfig.ConfigReport {
	t.Helper()
	var r apiconfig.ConfigReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not an api config document: %v\n%s", err, out)
	}
	return r
}

func decodeChange(t *testing.T, out string) apiconfig.ChangeResult {
	t.Helper()
	var r apiconfig.ChangeResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not an api config change document: %v\n%s", err, out)
	}
	return r
}

// fakeAutostart is the service manager: installed is whether the daemon is registered.
type fakeAutostart struct{ installed bool }

func (f *fakeAutostart) Install(context.Context) (autostart.Result, error) {
	return autostart.Result{}, nil
}
func (f *fakeAutostart) Uninstall(context.Context) error { return nil }
func (f *fakeAutostart) Status(context.Context) (bool, string) {
	return f.installed, "/x/monoagent-daemon.service"
}
func (f *fakeAutostart) Start(context.Context) error   { return nil }
func (f *fakeAutostart) Restart(context.Context) error { return nil }
