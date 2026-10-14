package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/apikeys"
	"github.com/monoes/mono-agent/internal/testdb"
)

// newAPITestDB is a migrated database and the HOME that goes with it.
func newAPITestDB(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MONOAGENT_API_CONTEXT_CONFINEMENT", "")
	return testdb.Path(t)
}

// runAPI runs `api <args>` and returns stdout and stderr separately.
func runAPI(t *testing.T, dbPath, profile string, jsonOut bool, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// `revoke` asks only on a terminal; tests never have one.
	wasTerminal := apiStdinIsTerminal
	apiStdinIsTerminal = func() bool { return false }
	defer func() { apiStdinIsTerminal = wasTerminal }()

	cmd := newAPICmd(&globalConfig{DBPath: dbPath, ProfileID: profile, JSONOutput: jsonOut})
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	err = cmd.Execute()
	return out.String(), errb.String(), err
}

// `api key` lives in internal/cli/apikey (tested there); this pins its adapter: the active
// profile, --json, the exit codes and the terminal check reach the commands.
func TestAPIKeyAdapterWiring(t *testing.T) {
	db := newAPITestDB(t)
	seedProfile(t, db, "work")
	stdout, _, err := runAPI(t, db, "work", false, "key", "create", "--name", "app")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(stdout), apikeys.KeyPrefix) {
		t.Fatalf("create: %q, %v", stdout, err)
	}
	out, _, err := runAPI(t, db, "work", true, "key", "list")
	var keys []apikeys.Key
	if err != nil || json.Unmarshal([]byte(out), &keys) != nil || len(keys) != 1 || keys[0].ProfileID != "work" {
		t.Fatalf("list --json as work: %q, %v", out, err)
	}
	if out, _, _ := runAPI(t, db, "default", true, "key", "list"); strings.TrimSpace(out) != "[]" {
		t.Errorf("the default profile sees work's key: %s", out)
	}
	if _, _, err := runAPI(t, db, "work", true, "key", "show", "ghost"); exitCode(err) != 2 {
		t.Errorf("show of an unknown key: exit %d (%v), want 2", exitCode(err), err)
	}
	if _, _, err := runAPI(t, db, "work", false, "key", "revoke", "app"); exitCode(err) != 3 {
		t.Errorf("revoke without --yes off a terminal: exit %d (%v), want 3", exitCode(err), err)
	}
}
