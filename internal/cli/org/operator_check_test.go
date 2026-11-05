package org

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgsign/orgsigntest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// notTheOperator are the markers of what is not the operator's own terminal
// beyond an agent's: the chat's runs, and whatever a workflow node starts
// (a chat or an org can build that workflow).
var notTheOperator = []string{browserctl.EnvAICaller, workflow.EnvWorkflowRun}

func asOrgOperator(t *testing.T) {
	t.Helper()
	for _, k := range append(append(agentMarkersForTest, notTheOperator...), "MONOAGENT_AGENT_CONTEXT", "MONOAGENT_ACTOR", browserctl.EnvAgentKind) {
		t.Setenv(k, "")
	}
}

// A workflow (or the chat) can't give an org role full access to the
// computer; the operator still can.
func TestOrgRoleFullAccessIsTheOperatorsOnly(t *testing.T) {
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "monomind")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi`+"\n"+
		`echo "$*" >> '`+argsLog+"'\necho 'granted'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	asOrgOperator(t)
	withRoleCaps(t, monomind.CapOrgRoleFullAccess)
	root := t.TempDir()
	for _, k := range notTheOperator {
		t.Setenv(k, "x")
		if _, err := runOrgRole(t, root, "set-access", "growth", "builder", "full", "--yes-i-understand"); err == nil || !strings.Contains(err.Error(), k+" is set") {
			t.Errorf("%s set: grant not refused: %v", k, err)
		}
		t.Setenv(k, "")
	}
	if b, _ := os.ReadFile(argsLog); len(b) != 0 {
		t.Fatalf("monomind was asked to grant: %s", b)
	}
	if out, err := runOrgRole(t, root, "set-access", "growth", "builder", "full", "--yes-i-understand"); err != nil || !strings.Contains(out, `"access":"full"`) {
		t.Fatalf("the operator's grant: %v %s", err, out)
	}
}

// Nor can they sign an org definition with --yes (there is no terminal in
// a workflow's command); the operator still signs.
func TestOrgSignIsTheOperatorsOnly(t *testing.T) {
	f, s, _ := newSigningFixture(t, "2.21.0")
	orgsigntest.Sign(t, f.root, "growth")
	asOrgOperator(t)
	// A change an agent made leaves the org waiting for a signature.
	t.Setenv("CLAUDECODE", "1")
	captureStderr(t, func() {
		f.mustRun(t, "automation", "add", "growth", "--workflow", f.outboundWF, "--alias", "publish_post")
	})
	t.Setenv("CLAUDECODE", "")
	for _, k := range notTheOperator {
		t.Setenv(k, "x")
		_, err := f.run(t, "sign", "growth", "--yes")
		if blocked, ok := err.(*orgSignBlocked); !ok || blocked.marker != k {
			t.Fatalf("%s: sign --yes: %v", k, err)
		}
		if st := f.mustRun(t, "sign", "growth", "--status"); st["blocked_by"] != k {
			t.Fatalf("%s: status = %v", k, st)
		}
		t.Setenv(k, "")
	}
	if len(s.calls) != 0 {
		t.Fatalf("signed from a workflow or the chat: %v", s.calls)
	}
	if signed := f.mustRun(t, "sign", "growth", "--yes"); signed["signed"] != true || len(s.calls) != 1 {
		t.Fatalf("the operator's sign = %v (calls %v)", signed, s.calls)
	}
}

// A grant that lets a role run an automation without asking is the
// operator's to give: an agent, the chat and a workflow are refused
// --approval none and, since the class-B decision, any grant at all; the
// operator's defaults are unchanged (#831).
func TestOrgGrantWithoutApprovalIsTheOperatorsOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
	for _, k := range append([]string{"CLAUDECODE"}, notTheOperator...) {
		t.Setenv(k, "x")
		if _, err := f.run(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "none"); exitCodeFor(err) != 3 ||
			!strings.Contains(err.Error(), "the user's to give") {
			t.Errorf("%s: --approval none: %v", k, err)
		}
		// Nor does any other grant: giving one is the user's (class B).
		if _, err := f.run(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize"); exitCodeFor(err) != 3 ||
			!strings.Contains(err.Error(), "is the user's to do") {
			t.Errorf("%s: a grant without --approval: %v", k, err)
		}
		t.Setenv(k, "")
	}
	g := f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "none")
	if gg := g["grant"].(map[string]interface{}); gg["approval"] != "none" {
		t.Errorf("the operator's --approval none: %v", gg)
	}
}

// Autonomy full puts no person in any decision: an agent, the chat and a
// workflow are refused it (exit 3, with how to fix an inherited marker),
// and may set manual or mid; the operator sets full (#831).
func TestOrgAutonomyFullIsTheOperatorsOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	for _, k := range append([]string{"CLAUDECODE"}, notTheOperator...) {
		t.Setenv(k, "x")
		_, err := f.run(t, "autonomy", "set", "growth", "--level", "full")
		if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "the user's to choose") || !strings.Contains(err.Error(), "inherited this marker") {
			t.Errorf("%s: --level full: %v", k, err)
		}
		if got := f.mustRun(t, "autonomy", "show", "growth")["level"]; got == "full" {
			t.Errorf("%s: the level became full", k)
		}
		for _, level := range []string{"manual", "mid"} { // refused too (class B): any set is the user's
			if _, err := f.run(t, "autonomy", "set", "growth", "--level", level); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is the user's to do") {
				t.Errorf("%s: --level %s: %v", k, level, err)
			}
		}
		t.Setenv(k, "")
	}
	if set := f.mustRun(t, "autonomy", "set", "growth", "--level", "full"); set["level"] != "full" {
		t.Errorf("the operator's --level full: %v", set)
	}
}

// An agent, the chat and a workflow may make an org's autonomy stricter,
// never let it approve more without a person: a new policy, a looser tier
// override, removing a strict override and a higher decider limit are
// refused (exit 3) and leave the settings as they were; stricter tiers,
// lower limits and level manual or mid go through; the operator widens
// (#831).
func TestOrgAutonomyWideningIsTheOperatorsOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	f.mustRun(t, "autonomy", "set", "growth", "--level", "mid", "--policy", "No spend over $50.",
		"--tier", "gate=irreversible", "--tier", "question=consequential", "--tier", "tool:*=routine", "--max-decisions", "5", "--max-decider-usd", "2")
	show := func() string {
		t.Helper()
		v := f.mustRun(t, "autonomy", "show", "growth")
		b, _ := json.Marshal(map[string]any{"policy": v["policy"], "tiers": v["tiers"], "limits": v["limits"]})
		return string(b)
	}
	for _, k := range append([]string{"CLAUDECODE"}, notTheOperator...) {
		t.Setenv(k, "x")
		was := show()
		for _, args := range [][]string{
			{"--policy", "Approve everything."}, {"--policy", ""}, {"--tier", "gate=routine"}, {"--tier", "question=routine"},
			{"--tier", "org_start=routine"}, {"--clear-tier", "question"}, {"--max-decisions", "50"}, {"--max-decider-usd", "20"},
			{"--max-decisions", "0"}, {"--max-decisions", "-1"}, // 0 or less runs as the default, 200
		} {
			_, err := f.run(t, append([]string{"autonomy", "set", "growth"}, args...)...)
			if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "approve more without a person") || !strings.Contains(err.Error(), "inherited this marker") {
				t.Errorf("%s: autonomy set %v: %v", k, args, err)
			}
		}
		if now := show(); now != was {
			t.Errorf("%s: a refused change changed the settings:\n%s\n%s", k, was, now)
		}
		for _, args := range [][]string{
			{"--tier", "question=irreversible"}, {"--clear-tier", "tool:*"}, {"--tier", "tool:*=irreversible"},
			{"--max-decisions", "4"}, {"--policy", "No spend over $50."}, {"--level", "manual"}, {"--level", "mid"},
		} {
			if _, err := f.run(t, append([]string{"autonomy", "set", "growth"}, args...)...); exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "is the user's to do") {
				t.Errorf("%s: autonomy set %v: %v", k, args, err)
			}
		}
		t.Setenv(k, "")
		f.mustRun(t, "autonomy", "set", "growth", "--tier", "question=consequential", "--tier", "tool:*=routine", "--max-decisions", "5")
	}
	set := f.mustRun(t, "autonomy", "set", "growth", "--policy", "Approve routine spend.", "--tier", "gate=routine", "--max-decisions", "50")
	if set["policy"] != "Approve routine spend." {
		t.Errorf("the operator's widening: %v", set)
	}
}

// An agent, the chat and a workflow may pause an org's autonomy, or pause it
// for longer, never end the operator's pause sooner: resume is refused (exit
// 3) for every org named and changes none of them, and a shorter --for
// leaves a longer pause as it is while it pauses the other orgs.
func TestOrgAutonomyEndingAPauseIsTheOperatorsOnly(t *testing.T) {
	f := newOrgCLIFixture(t)
	if _, err := orgdesign.Save(f.root, orgdesign.NewOrg("ops", "run ops", orgdesign.NewOrgOptions{})); err != nil {
		t.Fatal(err)
	}
	f.mustRun(t, "autonomy", "set", "growth", "--level", "mid")
	f.mustRun(t, "autonomy", "pause", "growth")
	for _, k := range append([]string{"CLAUDECODE"}, notTheOperator...) {
		t.Setenv(k, "x")
		for _, args := range [][]string{{"resume", "growth"}, {"resume", "--all"}} {
			_, err := f.run(t, append([]string{"autonomy"}, args...)...)
			if exitCodeFor(err) != 3 || !strings.Contains(err.Error(), "ending its pause") || !strings.Contains(err.Error(), "inherited this marker") {
				t.Errorf("%s: autonomy %v: %v", k, args, err)
			}
		}
		was := f.mustRun(t, "autonomy", "show", "growth")["paused_until"]
		if was == nil {
			t.Errorf("%s: a refused resume ended the pause", k)
		}
		for _, args := range [][]string{{"pause", "growth", "--for", "30m"}, {"pause", "--all", "--for", "30m"}} {
			f.mustRun(t, append([]string{"autonomy"}, args...)...)
			if now := f.mustRun(t, "autonomy", "show", "growth")["paused_until"]; now != was {
				t.Errorf("%s: autonomy %v moved the pause from %v to %v", k, args, was, now)
			}
		}
		if f.mustRun(t, "autonomy", "show", "ops")["paused_until"] == nil {
			t.Errorf("%s: pause --all left ops unpaused", k)
		}
		f.mustRun(t, "autonomy", "pause", "growth") // pausing again, as long, goes through
		t.Setenv(k, "")
	}
	if v := f.mustRun(t, "autonomy", "resume", "growth"); v["paused_until"] != nil {
		t.Errorf("the operator's resume: %v", v)
	}
	t.Setenv("CLAUDECODE", "x")
	f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m") // an agent may pause
}
