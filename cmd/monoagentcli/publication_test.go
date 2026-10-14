package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/publication"
)

// `publication` lives in internal/cli/publication (tested there); this pins its adapter: the
// active profile, --json, the exit codes and the real operator check (MONOAGENT_ACTOR) reach the
// commands. The approval of an agent's register is pinned by hil_send_test.go.
func TestPublicationAdapterWiring(t *testing.T) {
	asOperatorProcess(t)
	dbPath := newApplicationCLITestDB(t)
	seedProfile(t, dbPath, "work")
	run := func(input string, args ...string) (string, error) {
		cmd := newPublicationCmd(&globalConfig{DBPath: dbPath, ProfileID: "work", JSONOutput: true})
		var out, errb bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errb)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	out, err := run(`{"platform":"x","kind":"post","body":"text"}`, "register", "--stdin-json")
	var e publication.Entry
	if err != nil || json.Unmarshal([]byte(out), &e) != nil || e.ProfileID != "work" {
		t.Fatalf("register as work: %s %v", out, err)
	}
	if _, err := run("", "get", "missing"); exitCode(err) != 2 {
		t.Errorf("get of an unknown publication: exit %d (%v), want 2", exitCode(err), err)
	}
	if _, err := run("", "list", "--limit", "0"); exitCode(err) != 3 {
		t.Errorf("list --limit 0: exit %d (%v), want 3", exitCode(err), err)
	}
	t.Setenv("MONOAGENT_ACTOR", "writer-bot")
	for _, args := range [][]string{{"delete", e.ID}, {"redact", e.ID}} {
		if _, err := run("", args...); exitCode(err) != 3 || !strings.Contains(err.Error(), "only the operator") {
			t.Errorf("%v by an agent: exit %d (%v), want the operator-only refusal", args, exitCode(err), err)
		}
	}
	t.Setenv("MONOAGENT_ACTOR", "")
	if out, err := run("", "delete", e.ID); err != nil || !strings.Contains(out, e.ID) {
		t.Errorf("delete by the operator: %s %v", out, err)
	}
}
