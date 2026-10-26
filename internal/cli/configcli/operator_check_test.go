package configcli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/storage"
)

// A Deps without its operator check refuses a reserved row with the
// operator-only code (exit 3, operator_only), and says the check is not
// wired; any other row is anyone's (#845 follow-up).
func TestConfigWithoutAnOperatorCheckRefusesReservedRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "config.db")
	d := Deps{
		JSONOutput: func() bool { return true },
		OpenDB: func() (*storage.Database, error) {
			db, err := storage.NewDatabase(dbPath)
			if err != nil {
				return nil, err
			}
			return db, db.ApplyMigrations()
		},
	}
	run := func(args ...string) error {
		cmd := NewCommand(d)
		cmd.SetOut(&strings.Builder{})
		cmd.SetErr(&strings.Builder{})
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		return cmd.Execute()
	}
	for _, args := range [][]string{{"set", "coder_mode", "{}"}, {"delete", "coder_mode"}} {
		err := run(args...)
		var fields interface{ JSONErrorFields() map[string]any }
		if clicore.ExitCodeFor(err) != 3 || !errors.As(err, &fields) || fields.JSONErrorFields()["code"] != "operator_only" ||
			!strings.Contains(err.Error(), "operator check is not wired") {
			t.Errorf("config %v without an operator check: %v", args, err)
		}
	}
	if err := run("set", "memory.runs", "off"); err != nil {
		t.Errorf("a row that isn't reserved: %v", err)
	}
}

// The rows under a reserved prefix hold the operator's mission confirmations
// (live single-use material): a caller that is not the operator can neither
// write, delete nor read them, in any spelling of the name, and a listing shows
// no value; the operator's terminal sees them as for any row.
func TestConfigRowsUnderAReservedPrefixAreTheOperatorsAlone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "config.db")
	open := func() (*storage.Database, error) {
		db, err := storage.NewDatabase(dbPath)
		if err != nil {
			return nil, err
		}
		return db, db.ApplyMigrations()
	}
	build := func(operator bool, json bool, _ *strings.Builder) Deps {
		d := Deps{JSONOutput: func() bool { return json }, OpenDB: open}
		d.RequireOperator = func(what string) error {
			if operator {
				return nil
			}
			return clicore.OperatorOnly("only the user can %s", what)
		}
		return d
	}
	run := func(d Deps, args ...string) (string, error) { // the commands print on os.Stdout
		r, w, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		real := os.Stdout
		os.Stdout = w
		cmd := NewCommand(d)
		cmd.SetOut(w)
		cmd.SetErr(w)
		cmd.SetArgs(args)
		cmd.SilenceErrors, cmd.SilenceUsage = true, true
		err := cmd.Execute()
		os.Stdout = real
		w.Close()
		b, _ := io.ReadAll(r)
		return string(b), err
	}
	row := "coder-mission-confirm/default/c1/t1/7"
	// the operator's own record
	if _, err := run(build(true, true, nil), "set", row, "SECRET-NONCE"); err != nil {
		t.Fatalf("the operator's write: %v", err)
	}
	agent := build(false, true, nil)
	for _, name := range []string{row, "Coder-Mission-Confirm/default/c1/t1/8", " CODER-MISSION-CONFIRM/x", "coder-mission-confirm/"} {
		for _, args := range [][]string{{"set", name, "forged"}, {"delete", name}, {"get", name}} {
			if _, err := run(agent, args...); clicore.ExitCodeFor(err) != 3 {
				t.Errorf("config %v by a non-operator: %v", args, err)
			}
		}
	}
	db, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	var val string
	if err := db.DB.QueryRow(`SELECT COUNT(*), COALESCE(MAX(config_data), '') FROM configs WHERE name LIKE 'coder-mission-confirm/%' OR lower(trim(name)) LIKE 'coder-mission-confirm/%'`).Scan(&n, &val); err != nil || n != 1 || val != "SECRET-NONCE" {
		t.Fatalf("rows under the prefix after the refused writes: n=%d value=%q %v", n, val, err)
	}
	// a listing shows the row without its value to a non-operator, with it to the operator
	if out, err := run(agent, "list"); err != nil || strings.Contains(out, "SECRET-NONCE") || !strings.Contains(out, row) {
		t.Errorf("list for a non-operator: %v\n%s", err, out)
	}
	if out, err := run(build(true, true, nil), "list"); err != nil || !strings.Contains(out, "SECRET-NONCE") {
		t.Errorf("list for the operator: %v\n%s", err, out)
	}
	if out, err := run(build(true, true, nil), "get", row); err != nil || !strings.Contains(out, "SECRET-NONCE") {
		t.Errorf("get for the operator: %v\n%s", err, out)
	}
	// without any operator check the prefix is refused as well
	if _, err := run(Deps{JSONOutput: func() bool { return true }, OpenDB: open}, "set", row, "x"); clicore.ExitCodeFor(err) != 3 {
		t.Errorf("set without an operator check: %v", err)
	}
}
