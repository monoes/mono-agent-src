package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	orgFullFmt   = "autonomy full lets org growth's decider handle everything with no person involved, which is the user's to choose: %s; set manual or mid, " + orgRefusedTail
	orgWidenFmt  = "--max-decisions 50 (now 5) would let org growth approve more without a person, which is the user's to choose: %s; make it stricter or leave it, " + orgRefusedTail
	orgNoneFmt   = `a grant with --approval none lets role "lead" run "summarize" without asking, which is the user's to give: %s; leave --approval out (or pass required), ` + orgRefusedTail
	orgApproveFm = "approving " + orgApproveSend + " lets a message to a person go out, which only the user approves: %s; deny it, or approve it yourself from a terminal or the app"
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
			checkWhy(t, err, ctx, orgWidenFmt)
			if got := fmt.Sprint(showAutonomy(t, f, "limits")); !strings.Contains(got, "5") || strings.Contains(got, "50") {
				t.Fatalf("a refused call changed the limits: %s", got)
			}
			// stricter stays anyone's
			if _, err := f.run(t, "autonomy", "set", "growth", "--max-decisions", "4"); err != nil {
				t.Fatalf("a stricter limit was refused: %v", err)
			}
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
			if err == nil || exitCodeFor(err) != 3 || !strings.HasPrefix(err.Error(), "ending its pause (until ") {
				t.Fatalf("resume: %v", err)
			}
			wantTail := " sooner would let org growth approve more without a person, which is the user's to choose: "
			if !strings.Contains(err.Error(), wantTail) || !strings.HasSuffix(err.Error(), "; make it stricter or leave it, "+orgRefusedTail) {
				t.Fatalf("resume text: %s", err)
			}
			if ctx.want != nil && !strings.Contains(err.Error(), wantTail+ctx.want()+"; make") {
				t.Fatalf("%s: resume text carries another why: %s", ctx.name, err)
			}
			if ctx.wantPart != "" && !strings.Contains(err.Error(), ctx.wantPart) {
				t.Fatalf("%s: resume text: %s", ctx.name, err)
			}
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
			// without --approval the grant asks first, as it does for a marked agent
			g := f.mustRun(t, "grant", "add", "growth", "--role", "lead", "--automation", "summarize")
			if gg := g["grant"].(map[string]interface{}); gg["approval"] != "required" {
				t.Fatalf("%s: a grant without --approval: %v", ctx.name, gg)
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
			// a variant of the pending action matches none: refused with the same why
			_, err = f.run(t, "approve", "growth", "writer", strings.ToUpper(orgApproveSend))
			if err == nil || !strings.Contains(err.Error(), "no pending approval of exactly") {
				t.Fatalf("%s: a typed variant: %v", ctx.name, err)
			}
			// denying, and approving one that sends nothing, stay anyone's
			for _, args := range [][]string{{"deny", "growth", "writer", orgApproveSend}, {"approve", "growth", "writer", "monoagent__automation_sum"}} {
				if _, err := f.run(t, args...); err != nil {
					t.Fatalf("%s: %v: %v", ctx.name, args, err)
				}
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

// The marked path is byte-identical: for a marked caller the registered turn-aware gate answers exactly what
// operator.Refusal did before these verbs asked opgate.
func TestMarkedCallersGetTheSameWhyFromTheTurnAwareGateAsFromTheMarkerCheck(t *testing.T) {
	for _, ctx := range orgGateContexts() {
		if !strings.HasPrefix(ctx.name, "marked") {
			continue
		}
		t.Run(ctx.name, func(t *testing.T) {
			f := orgGateFixture(t)
			ctx.setup(t, f)
			if got, want := opgate.Refusal(), operator.Refusal(); got == "" || got != want {
				t.Fatalf("turn-aware gate %q, marker check %q", got, want)
			}
		})
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
