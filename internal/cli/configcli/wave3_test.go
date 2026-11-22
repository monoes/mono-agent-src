package configcli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

// runConfig runs `config <args>` against a fresh database and returns what it printed on
// os.Stdout and its error. It is not parallel: it swaps os.Stdout.
func runConfig(t *testing.T, dbPath string, jsonOut bool, args ...string) (string, error) {
	t.Helper()
	d := Deps{
		JSONOutput: func() bool { return jsonOut },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			return db, db.ApplyMigrations()
		},
		RequireOperator: func(string) error { return nil },
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	cmd := NewCommand(d)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	runErr := cmd.Execute()
	w.Close()
	os.Stdout = real
	return <-done, runErr
}

func TestConfigNotFoundIsExitTwo(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	for _, args := range [][]string{{"get", "nope"}, {"delete", "nope"}} {
		_, err := runConfig(t, db, false, args...)
		if err == nil || clicore.ExitCodeFor(err) != 2 {
			t.Errorf("config %v: err %v, exit %d; want not-found (2)", args, err, clicore.ExitCodeFor(err))
		}
	}
}

func TestConfigDeleteHonoursJSON(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	if _, err := runConfig(t, db, false, "set", "max_retries", "3"); err != nil {
		t.Fatal(err)
	}
	out, err := runConfig(t, db, true, "delete", "max_retries")
	if err != nil || !strings.Contains(out, `"deleted": true`) || !strings.Contains(out, `"name": "max_retries"`) {
		t.Fatalf("out %q err %v; want a JSON document", out, err)
	}
}

func TestConfigListJSONWhenEmptyIsAnEmptyArray(t *testing.T) {
	out, err := runConfig(t, filepath.Join(t.TempDir(), "c.db"), true, "list")
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("out %q err %v; want []", out, err)
	}
}

// set must not echo the value (it may be a key) into logs and scrollback.
func TestConfigSetDoesNotEchoTheValue(t *testing.T) {
	db := filepath.Join(t.TempDir(), "c.db")
	for _, jsonOut := range []bool{false, true} {
		out, err := runConfig(t, db, jsonOut, "set", "some_name", "sk-secret-value-123")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "sk-secret-value-123") || !strings.Contains(out, "some_name") {
			t.Errorf("json=%v: output %q echoes the value or drops the name", jsonOut, out)
		}
	}
}
