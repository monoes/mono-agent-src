package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/monomind"
)

const turnStandIn = "stand-in: a model's shell inside a coder turn"

// asInATurn stands in for the turn-aware gate cmd/monoagentcli registers, for a process whose markers are cleared.
func asInATurn(t *testing.T) {
	t.Helper()
	asOrgOperator(t)
	opgate.SetForTest(t, func(bool) string { return turnStandIn })
}

// Every site that splits operator from agent asks opgate, not the marker check: a process that opgate refuses and
// that carries no marker gets the limited flavour (the real gate's cases are in cmd/monoagentcli).
func TestOrgChatApproveAsksTheTurnAwareGate(t *testing.T) {
	cfg, fake, _ := setupOrgChat(t)
	asInATurn(t)
	seedSendingGrants(t, cfg.DBPath, "acme", "dev")
	fake.lists["approvals"], _ = json.Marshal(map[string]interface{}{"v": 1, "org": "acme", "items": []map[string]interface{}{
		{"roleId": "dev", "action": "monoagent__automation_send", "question": "send it", "ts": 1700, "approved": nil, "requestId": "req-send"},
		{"roleId": "dev", "action": "monoagent__automation_sum", "question": "sum it", "ts": 1701, "approved": nil, "requestId": "req-sum"},
	}})
	if _, err := runOrgChat(t, cfg, "approve", "acme", "req-send"); exitCodeFor(err) != 3 ||
		!strings.Contains(err.Error(), "which only the user approves: "+turnStandIn+"; deny it") {
		t.Fatalf("chat approve of a sending automation: %v", err)
	}
	if len(fake.sent) != 0 {
		t.Fatalf("sent %v", fake.sent)
	}
	if _, err := runOrgChat(t, cfg, "approve", "acme", "req-sum"); err != nil || len(fake.sent) != 1 {
		t.Fatalf("approving one that sends nothing: %v %v", err, fake.sent)
	}
}

func TestOrgAutonomyAndGrantAddAskTheTurnAwareGate(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	f.mustRun(t, "autonomy", "set", "growth", "--level", "mid", "--max-decisions", "5")
	f.mustRun(t, "autonomy", "pause", "growth")
	was := f.mustRun(t, "autonomy", "show", "growth")["paused_until"]
	asInATurn(t)
	for name, args := range map[string][]string{
		"full":      {"autonomy", "set", "growth", "--level", "full"},
		"widening":  {"autonomy", "set", "growth", "--max-decisions", "50"},
		"resume":    {"autonomy", "resume", "growth"},
		"grantnone": {"grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "none"},
	} {
		if _, err := f.run(t, args...); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), turnStandIn+"; ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m")
	if now := f.mustRun(t, "autonomy", "show", "growth")["paused_until"]; now != was {
		t.Errorf("a shorter pause moved the pause from %v to %v", was, now)
	}
	if g := f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize")["grant"].(map[string]interface{}); g["approval"] != "required" {
		t.Errorf("a grant without --approval: %v", g)
	}
}

func TestOrgApproveAsksTheTurnAwareGate(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "monomind")
	pending := `{"v":1,"items":[` +
		`{"roleId":"writer","action":"monoagent__automation_send","question":"q","ts":1,"approved":null,"requestId":"r1"},` +
		`{"roleId":"writer","action":"monoagent__automation_sum","question":"q","ts":2,"approved":null,"requestId":"r2"}]}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi`+"\n"+
		`case "$*" in *"org approvals"*) echo '`+pending+`'; exit 0 ;; esac`+"\n"+
		`echo "$*" >> '`+argsLog+"'\necho '{\"v\":1,\"ok\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	t.Setenv("HOME", t.TempDir())
	asInATurn(t)
	cfg := &testConfig{DBPath: filepath.Join(t.TempDir(), "o.db"), ProfileID: "default"}
	seedSendingGrants(t, cfg.DBPath, "growth", "writer")
	run := func(args ...string) error {
		var err error
		captureStdout(t, func() {
			cmd := newOrgCmd(cfg)
			cmd.SetArgs(args)
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			err = cmd.Execute()
		})
		return err
	}
	if err := run("approve", "growth", "writer", "monoagent__automation_send"); exitCodeFor(err) != 3 ||
		!strings.Contains(err.Error(), "which only the user approves: "+turnStandIn+"; deny it") {
		t.Fatalf("approve of a sending automation: %v", err)
	}
	if err := run("approve", "growth", "writer", "MONOAGENT__AUTOMATION_SEND"); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "no pending approval of exactly") ||
		!strings.Contains(err.Error(), turnStandIn) {
		t.Fatalf("approve of a variant: %v", err)
	}
	if b, _ := os.ReadFile(argsLog); strings.Contains(strings.ToLower(string(b)), "automation_send") {
		t.Fatalf("a refused approval reached monomind:\n%s", b)
	}
	if err := run("approve", "growth", "writer", "monoagent__automation_sum"); err != nil {
		t.Fatalf("approving one that sends nothing: %v", err)
	}
}
