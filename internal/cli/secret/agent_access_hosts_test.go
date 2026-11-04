package secret

import (
	"strings"
	"testing"
)

// A grant cannot name an entry no agent reads: httpapi-token (the HTTP API's
// bearer) and agent-token-*. Refused at creation, whatever the case or
// spaces, with nothing turned on.
func TestAgentAccessOnRefusesReservedEntryNames(t *testing.T) {
	db := newSecretCLITestDB(t)
	profileP1(t, db)
	for _, name := range []string{"httpapi-token", " HTTPAPI-Token ", "agent-token-ci", "Agent-Token-x", "ok,httpapi-token"} {
		for _, args := range [][]string{
			{"agent-access", "on", "--yes", "--any-host", "--secret", name},
			{"agent-access", "use", "--yes", "--host", "a.example", "--secret", name},
		} {
			_, err := runSecretCmd(t, db, args...)
			if err == nil || !strings.Contains(err.Error(), "never read by an agent") {
				t.Errorf("%v: %v", args, err)
			}
		}
	}
	if out, _ := runSecretCmd(t, db, "agent-access"); !strings.Contains(out, `"agent_access": false`) {
		t.Fatalf("a refused grant turned access on: %s", out)
	}
	if _, err := runSecretCmd(t, db, "agent-access", "on", "--yes", "--any-host", "--secret", "github"); err != nil {
		t.Errorf("an ordinary secret: %v", err)
	}
}

// status says a grant has no host binding, in the text and with added JSON
// fields (the existing ones are unchanged).
func TestAgentAccessStatusFlagsGrantsWithNoHostBinding(t *testing.T) {
	db := newSecretCLITestDB(t)
	profileP1(t, db)
	json := func() string { out, _ := runSecretCmd(t, db, "agent-access"); return out }
	text := func() string {
		out, err := runSecretCmdWith(testDeps(db, "p1", false), "agent-access")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := runSecretCmd(t, db, "agent-access", "on", "--yes", "--any-host"); err != nil {
		t.Fatal(err)
	}
	if out := json(); !strings.Contains(out, `"any_host": true`) || !strings.Contains(out, `"host_binding": "none"`) {
		t.Errorf("--any-host: %s", out)
	}
	if out := json(); !strings.Contains(out, `"carrying_runs_refuse_unchecked_nodes": false`) {
		t.Errorf("--any-host json: %s", out)
	}
	if out := text(); strings.Contains(out, "refuses node types") || !strings.Contains(out, "ANY (no host binding") {
		t.Errorf("--any-host text: %s", out)
	}
	if _, err := runSecretCmd(t, db, "agent-access", "on", "--yes", "--host", "a.example"); err != nil {
		t.Fatal(err)
	}
	if out := json(); !strings.Contains(out, `"any_host": false`) || !strings.Contains(out, `"host_binding": "hosts"`) {
		t.Errorf("--host: %s", out)
	}
	if out := json(); !strings.Contains(out, `"carrying_runs_refuse_unchecked_nodes": true`) {
		t.Errorf("--host json: %s", out)
	}
	if out := text(); !strings.Contains(out, "refuses node types whose destinations cannot be checked") {
		t.Errorf("--host text lacks the note: %s", out)
	}
	if out := text(); strings.Contains(out, "no host binding") || !strings.Contains(out, "a.example") {
		t.Errorf("--host text: %s", out)
	}
}
