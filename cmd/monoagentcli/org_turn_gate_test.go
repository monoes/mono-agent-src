package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orggrant"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/workflow"
)

// The org verbs the chat legitimately drives (`org approve` and `org chat approve`, `org autonomy set|pause|resume`,
// `org grant add`) split operator from agent on opgate.Refusal: a MARKED caller keeps the limited flavour it always
// had, byte for byte, and a process inside a coder turn (or holding an agent token) that cleared its markers now gets
// the same limits. The operator's own terminal is unchanged.

const (
	orgTurnWhy       = "this command runs inside a coder session's turn, so a model is running it"
	orgUnreadableWhy = "cannot tell whether a coder session's turn runs this command ("
	orgRunTail       = "run it yourself from a terminal or the app"
	orgRefusedTail   = "or run it yourself from a terminal or the app"
	orgApproveSend   = "monoagent__automation_send"
)

// orgGateContext puts the process into one context after the operator prepared the org. want is the exact why the
// guards print, or "" when only wantPart is checked.
type orgGateContext struct {
	name     string
	setup    func(t *testing.T, f *orgCLIFixture)
	want     func() string
	wantPart string
}

func orgGateContexts() []orgGateContext {
	return []orgGateContext{
		{name: "marked agent", setup: func(t *testing.T, _ *orgCLIFixture) { asAgentProcess(t) }, want: operator.Refusal},
		{name: "marked chat", setup: func(t *testing.T, _ *orgCLIFixture) {
			asOperatorProcess(t)
			t.Setenv(browserctl.EnvAICaller, browserctl.KindChat)
		}, want: operator.Refusal},
		{name: "marked workflow", setup: func(t *testing.T, _ *orgCLIFixture) {
			asOperatorProcess(t)
			t.Setenv(workflow.EnvWorkflowRun, "exec-1")
		}, want: operator.Refusal},
		{name: "in a turn, markers cleared", setup: func(t *testing.T, f *orgCLIFixture) {
			needAncestry(t)
			markThisProcessAsACoderTurn(t, f.cfg)
		}, want: func() string { return orgTurnWhy }},
		{name: "in a turn of a registered host, naming another database", setup: func(t *testing.T, _ *orgCLIFixture) {
			registeredTurnDB(t) // the fixture's database holds no turn: the registry finds it
		}, want: func() string { return orgTurnWhy }},
		{name: "token holder", setup: func(t *testing.T, _ *orgCLIFixture) {
			t.Setenv(coderhost.SessionTokenEnv, "session-token")
		}, want: func() string {
			return coderhost.SessionTokenEnv + " is set, so a process holding an agent token is running this command"
		}},
		{name: "database unreadable", setup: func(t *testing.T, f *orgCLIFixture) {
			withoutCoderSessionsColumn(t, f.cfg.DBPath)
		}, wantPart: orgUnreadableWhy},
	}
}

// orgGateFixture is an org CLI fixture with the production gate bound to it and the operator's environment.
func orgGateFixture(t *testing.T) *orgCLIFixture {
	t.Helper()
	f := newOrgCLIFixture(t)
	asOperatorProcess(t)
	opgate.SetForTest(t, f.cfg.gateRefusal)
	return f
}

// checkWhy asserts err is msg with why interpolated (exact for a fixed why, a prefix and tail otherwise).
func checkWhy(t *testing.T, err error, ctx orgGateContext, format string) {
	t.Helper()
	if err == nil || exitCodeFor(err) != 3 {
		t.Fatalf("%s: not refused (exit 3): %v", ctx.name, err)
	}
	if ctx.want != nil {
		if want := fmt.Sprintf(format, ctx.want()); err.Error() != want {
			t.Fatalf("%s: refusal text differs\n got: %s\nwant: %s", ctx.name, err.Error(), want)
		}
		return
	}
	i := strings.Index(format, "%s")
	if !strings.HasPrefix(err.Error(), format[:i]) || !strings.Contains(err.Error(), ctx.wantPart) || !strings.HasSuffix(err.Error(), format[i+2:]) {
		t.Fatalf("%s: refusal text: %s", ctx.name, err.Error())
	}
}

const (
	orgFullFmt    = "autonomy full lets org growth's decider handle everything with no person involved, which is the user's to choose: %s; set manual or mid, " + orgRefusedTail
	orgSetFmt     = "changing org growth's autonomy is the user's to do: %s; " + orgRunTail
	orgGrantFmt   = `granting "summarize" to role "lead" is the user's to do: %s; ` + orgRunTail
	orgApproveFm  = "approving " + orgApproveSend + " by writer in org growth is the user's to do: %s; " + orgRunTail
	orgApproveSum = "approving monoagent__automation_sum by writer in org growth is the user's to do: %s; " + orgRunTail
	orgGateFmt    = "approving gate-9 in org growth is the user's to do: %s; " + orgRunTail
	orgResumeFmt  = "resuming autonomy is the user's to do: %s; " + orgRunTail
	orgNoneFmt    = `a grant with --approval none lets role "lead" run "summarize" without asking, which is the user's to give: %s; leave --approval out (or pass required), ` + orgRefusedTail
)

func TestOrgAutonomyFullIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := orgGateFixture(t)
			f.mustRun(t, "autonomy", "set", "growth", "--level", "mid")
			ctx.setup(t, f)
			_, err := f.run(t, "autonomy", "set", "growth", "--level", "full")
			checkWhy(t, err, ctx, orgFullFmt)
			if got := showAutonomy(t, f, "level"); got != "mid" {
				t.Fatalf("a refused call changed the level to %v", got)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := orgGateFixture(t)
		if set := f.mustRun(t, "autonomy", "set", "growth", "--level", "full"); set["level"] != "full" {
			t.Fatalf("the operator's full: %v", set)
		}
	})
}

func TestOrgAutonomyWideningIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := orgGateFixture(t)
			f.mustRun(t, "autonomy", "set", "growth", "--level", "mid", "--max-decisions", "5")
			ctx.setup(t, f)
			_, err := f.run(t, "autonomy", "set", "growth", "--max-decisions", "50")
			checkWhy(t, err, ctx, orgSetFmt)
			if got := fmt.Sprint(showAutonomy(t, f, "limits")); !strings.Contains(got, "5") || strings.Contains(got, "50") {
				t.Fatalf("a refused call changed the limits: %s", got)
			}
			// a stricter one is refused too: any autonomy set is the user's (class B)
			_, err = f.run(t, "autonomy", "set", "growth", "--max-decisions", "4")
			checkWhy(t, err, ctx, orgSetFmt)
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := orgGateFixture(t)
		f.mustRun(t, "autonomy", "set", "growth", "--level", "mid", "--max-decisions", "5")
		f.mustRun(t, "autonomy", "set", "growth", "--max-decisions", "50")
	})
}

func TestOrgAutonomyEndingAPauseSoonerIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := orgGateFixture(t)
			f.mustRun(t, "autonomy", "set", "growth", "--level", "mid")
			f.mustRun(t, "autonomy", "pause", "growth")
			was := showAutonomy(t, f, "paused_until")
			ctx.setup(t, f)
			// resume is refused and leaves the pause
			_, err := f.run(t, "autonomy", "resume", "growth")
			checkWhy(t, err, ctx, orgResumeFmt)
			// a shorter pause leaves the longer one
			f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m")
			if now := showAutonomy(t, f, "paused_until"); now != was || now == nil {
				t.Fatalf("a refused caller moved the pause from %v to %v", was, now)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := orgGateFixture(t)
		f.mustRun(t, "autonomy", "pause", "growth")
		f.mustRun(t, "autonomy", "pause", "growth", "--for", "30m")
		if until, _ := showAutonomy(t, f, "paused_until").(string); until == "" || func() bool { p, _ := time.Parse(time.RFC3339, until); return time.Until(p) > time.Hour }() {
			t.Fatalf("the operator's shorter pause was kept longer: %v", until)
		}
		if v := f.mustRun(t, "autonomy", "resume", "growth"); v["paused_until"] != nil {
			t.Fatalf("the operator's resume: %v", v)
		}
	})
}

func TestOrgGrantAddWithoutApprovalIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f := orgGateFixture(t)
			f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
			ctx.setup(t, f)
			_, err := f.run(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize", "--approval", "none")
			checkWhy(t, err, ctx, orgNoneFmt)
			if n := grantCount(t, f); n != 0 {
				t.Fatalf("a refused call created %d grant(s)", n)
			}
			// no grant at all is the caller's (class B), whatever its approval mode
			_, err = f.run(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize")
			checkWhy(t, err, ctx, orgGrantFmt)
			if n := grantCount(t, f); n != 0 {
				t.Fatalf("a refused call created %d grant(s)", n)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f := orgGateFixture(t)
		f.mustRun(t, "automation", "add", "growth", "--workflow", f.plainWF, "--alias", "summarize")
		g := f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize")
		if gg := g["grant"].(map[string]interface{}); gg["approval"] != "none" {
			t.Fatalf("the operator's default grant: %v", gg)
		}
	})
}

func TestOrgApproveOfASendingAutomationIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f, argsLog := orgApproveFixture(t)
			ctx.setup(t, f)
			_, err := f.run(t, "approve", "growth", "writer", orgApproveSend)
			checkWhy(t, err, ctx, orgApproveFm)
			if approvedSend(argsLog) {
				t.Fatal("a refused approval reached monomind")
			}
			// approving one that sends nothing is refused too; denying stays anyone's
			_, err = f.run(t, "approve", "growth", "writer", "monoagent__automation_sum")
			checkWhy(t, err, ctx, orgApproveSum)
			if b, _ := os.ReadFile(argsLog); strings.Contains(string(b), "org approve") {
				t.Fatalf("%s: a refused approval reached monomind:\n%s", ctx.name, b)
			}
			if _, err := f.run(t, "deny", "growth", "writer", orgApproveSend); err != nil {
				t.Fatalf("%s: deny: %v", ctx.name, err)
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f, argsLog := orgApproveFixture(t)
		if _, err := f.run(t, "approve", "growth", "writer", orgApproveSend); err != nil || !approvedSend(argsLog) {
			t.Fatalf("the operator's approval: %v", err)
		}
	})
}

// The marked path is byte-identical: for a marked caller, whatever combination of markers it carries (and a
// token on top), the registered turn-aware gate answers exactly what operator.Refusal did before these verbs
// asked opgate. The markers come from operator.MarkerEnv; every single one and every pair is tried, alone and
// together with the chat and the workflow markers, with the values the real callers use.
func TestMarkedCallersGetTheSameWhyFromTheTurnAwareGateAsFromTheMarkerCheck(t *testing.T) {
	f := orgGateFixture(t)
	names := operator.MarkerEnv()
	clear := func(t *testing.T) {
		for _, k := range names {
			t.Setenv(k, "")
		}
	}
	var combos [][]string
	for i, a := range names {
		combos = append(combos, []string{a})
		for _, b := range names[i+1:] {
			combos = append(combos, []string{a, b})
		}
	}
	for _, extra := range [][]string{nil, {browserctl.EnvAICaller}, {workflow.EnvWorkflowRun}, {browserctl.EnvAICaller, workflow.EnvWorkflowRun}} {
		for _, token := range []bool{false, true} {
			for _, c := range combos {
				t.Run("", func(t *testing.T) {
					clear(t)
					for _, k := range append(append([]string{}, c...), extra...) {
						v := "x"
						if k == browserctl.EnvAICaller {
							v = browserctl.KindChat
						}
						t.Setenv(k, v)
					}
					if token {
						t.Setenv(coderhost.SessionTokenEnv, "session-token")
					}
					want := operator.Refusal()
					if want == "" {
						return // a combination that does not mark the caller is not this test's
					}
					if got := opgate.Refusal(); got != want {
						t.Fatalf("markers %v+%v token=%v\n got: %s\nwant: %s", c, extra, token, got, want)
					}
				})
			}
		}
	}
	_ = f
}

// Also for the registered body asked directly, and the recovery flavour: the marker text comes before any turn check.
func TestMarkedCallersGetTheMarkerTextFromEveryFlavourOfTheGate(t *testing.T) {
	f := orgGateFixture(t)
	asAgentProcess(t)
	want := operator.Refusal()
	if want == "" || f.cfg.gateRefusal(false) != want || f.cfg.gateRefusal(true) != want {
		t.Fatalf("flavours: %q %q want %q", f.cfg.gateRefusal(false), f.cfg.gateRefusal(true), want)
	}
}

// A MONOAGENT_ACTOR of only spaces is an agent without a name for the task callers and was no marker for
// operator.Refusal: it is limited now (an intended tightening, no flow exports one); an empty one is not set.
func TestABlankActorIsLimitedAndAnEmptyOneIsNotSet(t *testing.T) {
	f := orgGateFixture(t)
	opgate.SetForTest(t, f.cfg.gateRefusal)
	t.Setenv("MONOAGENT_ACTOR", "   ")
	if operator.Refusal() != "" || opgate.Refusal() == "" {
		t.Fatalf("marker check %q, gate %q", operator.Refusal(), opgate.Refusal())
	}
	t.Setenv("MONOAGENT_ACTOR", "")
	if opgate.Refusal() != "" {
		t.Fatalf("an empty MONOAGENT_ACTOR refused: %s", opgate.Refusal())
	}
}

func showAutonomy(t *testing.T, f *orgCLIFixture, key string) interface{} {
	t.Helper()
	// read through the store so a refused context's environment does not matter
	return f.mustRun(t, "autonomy", "show", "growth")[key]
}

func grantCount(t *testing.T, f *orgCLIFixture) int {
	t.Helper()
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gs, err := orggrant.NewStore(db.DB).ListGrants(context.Background(), "default", "growth", "lead")
	if err != nil {
		t.Fatal(err)
	}
	return len(gs)
}

func approvedSend(argsLog string) bool {
	b, _ := os.ReadFile(argsLog)
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "org approve ") && strings.Contains(strings.ToLower(line), orgApproveSend) {
			return true
		}
	}
	return false
}

// orgApproveFixture is an org fixture whose monomind lists two pending approvals of role writer (a call of a sending
// automation, a call of one that sends nothing) and logs what it is asked to settle.
func orgApproveFixture(t *testing.T) (*orgCLIFixture, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake monomind is a shell script")
	}
	f := orgGateFixture(t)
	dir := t.TempDir()
	argsLog := filepath.Join(dir, "args.log")
	bin := filepath.Join(dir, "monomind")
	pending := `{"v":1,"items":[` +
		`{"roleId":"writer","action":"` + orgApproveSend + `","question":"q","ts":1,"approved":null,"requestId":"r1"},` +
		`{"roleId":"writer","action":"monoagent__automation_sum","question":"q","ts":2,"approved":null,"requestId":"r2"}]}`
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+
		`if [ "$1" = "--version" ]; then echo '{"v":1,"version":"9.0.0","min_caller":"1.0.0","capabilities":["agent-exec","agent-scan","org-json-v1"]}'; exit 0; fi`+"\n"+
		`case "$*" in *"org approvals"*) echo '`+pending+`'; exit 0 ;; esac`+"\n"+
		`echo "$*" >> '`+argsLog+"'\necho '{\"v\":1,\"ok\":true}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(monomind.EnvOverride, bin)
	db, err := storage.NewDatabase(f.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	wfs := workflow.NewSQLiteWorkflowStore(db.DB)
	for alias, node := range map[string]workflow.WorkflowNode{
		"send": {Type: "comm.email_send", Name: "Email", Config: map[string]interface{}{"to": "ada@example.test", "subject": "Hi", "body": "Hello"}},
		"sum":  {Type: "core.set", Name: "Set"},
	} {
		id := "wf-" + alias
		node.ID, node.WorkflowID = id+"-n", id
		if err := wfs.CreateWorkflow(ctx, &workflow.Workflow{ID: id, Name: id, ProfileID: "default"}); err != nil {
			t.Fatal(err)
		}
		if err := wfs.SaveWorkflowNodes(ctx, id, []workflow.WorkflowNode{node}); err != nil {
			t.Fatal(err)
		}
		if _, err := orggrant.NewStore(db.DB).UpsertGrant(ctx, orggrant.GrantInput{ProfileID: "default", OrgName: "growth", RoleID: "writer",
			Tool: orggrant.Tool{Alias: alias, WorkflowID: id, Tier: orgdesign.TierConsequential}}); err != nil {
			t.Fatal(err)
		}
	}
	return f, argsLog
}

// Approving a decision gate is approving an approval (class B): `org gate-approve` is refused to a marked
// agent and to a process in a turn, with nothing reaching monomind; the operator's passes.
func TestOrgGateApproveIsRefusedToAMarkedAgentAndToAProcessInATurn(t *testing.T) {
	gateCalls := func(argsLog string) bool {
		b, _ := os.ReadFile(argsLog)
		return strings.Contains(string(b), "gate-approve")
	}
	for _, ctx := range orgGateContexts() {
		t.Run(ctx.name, func(t *testing.T) {
			f, argsLog := orgApproveFixture(t)
			ctx.setup(t, f)
			_, err := f.run(t, "gate-approve", "growth", "gate-9", "ship", "it")
			checkWhy(t, err, ctx, orgGateFmt)
			if gateCalls(argsLog) {
				t.Fatal("a refused gate approval reached monomind")
			}
		})
	}
	t.Run("operator", func(t *testing.T) {
		f, argsLog := orgApproveFixture(t)
		if _, err := f.run(t, "gate-approve", "growth", "gate-9", "ship", "it"); err != nil || !gateCalls(argsLog) {
			t.Fatalf("the operator's gate approval: %v", err)
		}
	})
}
