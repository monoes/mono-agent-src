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
	if _, err := runOrgChat(t, cfg, "approve", "acme", "req-sum"); exitCodeFor(err) != 3 ||
		err.Error() != "approving monoagent__automation_sum in org acme is the user's to do: "+turnStandIn+"; run it yourself from a terminal or the app" || len(fake.sent) != 0 {
		t.Fatalf("approving one that sends nothing is the user's too: %v %v", err, fake.sent)
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
		"full":         {"autonomy", "set", "growth", "--level", "full"},
		"widening":     {"autonomy", "set", "growth", "--max-decisions", "50"},
		"resume":       {"autonomy", "resume", "growth"},
		"grantnone":    {"grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "none"},
		"grantdefault": {"grant", "add", "growth", "--role", "lead", "--automation", "summarize"},
		"setmanual":    {"autonomy", "set", "growth", "--level", "manual"},
	} {
		if _, err := f.run(t, args...); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), turnStandIn+"; ") {
			t.Errorf("%s: %v", name, err)
		}
	}
	f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m")
	if now := f.mustRun(t, "autonomy", "show", "growth")["paused_until"]; now != was {
		t.Errorf("a shorter pause moved the pause from %v to %v", was, now)
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
		!strings.Contains(err.Error(), "is the user's to do: "+turnStandIn+"; ") {
		t.Fatalf("approve of a sending automation: %v", err)
	}
	if err := run("approve", "growth", "writer", "MONOAGENT__AUTOMATION_SEND"); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is the user's to do: "+turnStandIn) {
		t.Fatalf("approve of a variant: %v", err)
	}
	if b, _ := os.ReadFile(argsLog); strings.Contains(strings.ToLower(string(b)), "automation_send") {
		t.Fatalf("a refused approval reached monomind:\n%s", b)
	}
	if err := run("approve", "growth", "writer", "monoagent__automation_sum"); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), turnStandIn) {
		t.Fatalf("approving one that sends nothing is the user's too: %v", err)
	}
}

// `org sign --yes` without a terminal asks the turn-aware gate, not the marker alone: a process the gate
// refuses that carries no marker is refused like a marked agent, and signs nothing. A marked agent is
// refused even under a gate that says nothing, and an operator the gate does not refuse still signs.
func TestOrgSignYesAsksTheTurnAwareGate(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	asInATurn(t)
	_, err := f.run(t, "sign", "growth", "--yes")
	blocked, ok := err.(*orgSignBlocked)
	if !ok || blocked.marker != turnStandIn || blocked.JSONErrorFields()["code"] != "org_sign_agent_context" {
		t.Fatalf("cleared-marker turn: sign --yes: %v", err)
	}
	if st := f.mustRun(t, "sign", "growth", "--status"); st["blocked_by"] != turnStandIn {
		t.Fatalf("status = %v", st)
	}
	if len(s.calls) != 0 {
		t.Fatalf("a turn signed: %v", s.calls)
	}

	opgate.SetForTest(t, func(bool) string { return "" })
	t.Setenv("CODEX_THREAD_ID", "x")
	if _, err := f.run(t, "sign", "growth", "--yes"); err == nil {
		t.Fatal("a marked agent signed")
	}
	t.Setenv("CODEX_THREAD_ID", "")

	if signed := f.mustRun(t, "sign", "growth", "--yes"); signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("operator sign = %v (calls %v)", signed, s.calls)
	}
}

// A pty gives a turn a terminal, so the terminal does not decide who may sign: a process the gate refuses is
// refused --yes and the interactive confirmation alike, with a terminal or without; --status and the plain
// review stay open; the operator signs with a terminal and without one.
func TestOrgSignRefusedToTheGateWhateverTheTerminal(t *testing.T) {
	for _, tty := range []bool{false, true} {
		f, s, _ := newSigningFixture(t, "2.21.0")
		asInATurn(t)
		hash := f.mustRun(t, "sign", "growth")["hash"] // the plain review stays open
		f.cfg.TTY = tty
		if tty { // the interactive confirmation: a turn answering the prompt through a pty
			in := filepath.Join(t.TempDir(), "stdin")
			if err := os.WriteFile(in, []byte("y\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			fh, err := os.Open(in)
			if err != nil {
				t.Fatal(err)
			}
			prev := os.Stdin
			os.Stdin = fh
			t.Cleanup(func() { os.Stdin = prev; fh.Close() })
			if _, err := f.run(t, "sign", "growth"); !isSignBlocked(err) {
				t.Fatalf("tty=%v: interactive confirmation: %v", tty, err)
			}
		}
		if _, err := f.run(t, "sign", "growth", "--yes", "--expect-hash", hash.(string)); !isSignBlocked(err) {
			t.Fatalf("tty=%v: sign --yes --expect-hash: %v", tty, err)
		} else if strings.Contains(err.Error(), "no terminal") {
			t.Fatalf("tty=%v: the reason claims there is no terminal: %v", tty, err)
		}
		if st := f.mustRun(t, "sign", "growth", "--status"); st["blocked_by"] != turnStandIn {
			t.Fatalf("tty=%v: status %v", tty, st)
		}
		if len(s.calls) != 0 {
			t.Fatalf("tty=%v: a turn signed: %v", tty, s.calls)
		}
		// the operator: the gate says nothing.
		opgate.SetForTest(t, func(bool) string { return "" })
		if signed := f.mustRun(t, "sign", "growth", "--yes"); signed["signed"] != true || len(s.calls) != 1 {
			t.Fatalf("tty=%v: operator sign = %v (calls %v)", tty, signed, s.calls)
		}
	}
}

func isSignBlocked(err error) bool {
	b, ok := err.(*orgSignBlocked)
	return ok && b.JSONErrorFields()["code"] == "org_sign_agent_context"
}

// Approving a gate is approving an approval (class B, decided by the user): `org chat approve` of a
// gate- ref is the operator's only, like an approval's, and nothing reaches monomind when it is refused.
// Rejecting a gate stays open.
func TestOrgChatApproveOfAGateIsTheOperatorsOnly(t *testing.T) {
	want := func(why string) string {
		return "approving gate-1850-x1 in org acme is the user's to do: " + why + "; run it yourself from a terminal or the app"
	}
	t.Run("turn", func(t *testing.T) {
		cfg, fake, _ := setupOrgChat(t)
		asInATurn(t)
		_, err := runOrgChat(t, cfg, "approve", "acme", "gate-1850-x1", "--", "ship", "it")
		if exitCodeFor(err) != 3 || err.Error() != want(turnStandIn) || len(fake.sent) != 0 {
			t.Fatalf("a turn's approval of a gate: %v %v", err, fake.sent)
		}
		if _, err := runOrgChat(t, cfg, "deny", "acme", "gate-1850-x1"); err != nil || len(fake.sent) != 1 {
			t.Fatalf("rejecting a gate stays open: %v %v", err, fake.sent)
		}
	})
	t.Run("marked agent", func(t *testing.T) {
		cfg, fake, _ := setupOrgChat(t)
		asOrgOperator(t)
		t.Setenv("CLAUDECODE", "1")
		_, err := runOrgChat(t, cfg, "approve", "acme", "gate-1850-x1")
		if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "approving gate-1850-x1 in org acme is the user's to do: CLAUDECODE") || len(fake.sent) != 0 {
			t.Fatalf("a marked agent's approval of a gate: %v %v", err, fake.sent)
		}
	})
	t.Run("operator", func(t *testing.T) {
		cfg, fake, _ := setupOrgChat(t)
		asOrgOperator(t)
		if out, err := runOrgChat(t, cfg, "approve", "acme", "gate-1850-x1", "--", "ship", "it"); err != nil || !strings.Contains(out, `"state":"approved"`) || len(fake.sent) != 1 {
			t.Fatalf("the operator's approval of a gate: %v %s %v", err, out, fake.sent)
		}
	})
}
