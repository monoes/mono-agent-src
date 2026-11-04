package org

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/cli/opgate"
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
