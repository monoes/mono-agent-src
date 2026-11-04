package operator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/browserctl/browserctltest"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/workflow"
)

func asOperator(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), workflow.EnvWorkflowRun, browserctl.EnvAICaller,
		browserctl.EnvAgentKind, browserctl.EnvAgentKindToken) {
		t.Setenv(k, "")
	}
}

func TestMarker(t *testing.T) {
	asOperator(t)
	if m := Marker(); m != "" || Refusal() != "" {
		t.Fatalf("the operator's terminal: %q", m)
	}
	for k, want := range map[string]string{
		workflow.EnvWorkflowRun:          "a workflow run",
		browserctl.EnvAICaller:           "the chat",
		hilsend.EnvAgentContext:          "an agent",
		orgsign.AgentContextMarkers()[0]: "an agent",
		"MONOAGENT_ACTOR":                "an agent",
	} {
		asOperator(t)
		t.Setenv(k, "x")
		if m := Marker(); m != k || !strings.Contains(Refusal(), k+" is set, so "+want) {
			t.Errorf("%s: marker %q, refusal %q", k, m, Refusal())
		}
	}
}

// What is not the operator's terminal is named for what it leaves on record.
func TestCaller(t *testing.T) {
	asOperator(t)
	if c := Caller(); c != "" {
		t.Fatalf("the operator's terminal: %q", c)
	}
	for _, c := range []struct{ env, value, want string }{
		{workflow.EnvWorkflowRun, "exec-7", "workflow:exec-7"},
		{browserctl.EnvAICaller, "chat", "chat"},
		{browserctl.EnvAICaller, "something", "agent:other"},
		{hilsend.EnvAgentContext, "1", "agent:other"},
		{orgsign.AgentContextMarkers()[0], "1", "agent:other"},
	} {
		asOperator(t)
		t.Setenv(c.env, c.value)
		if got := Caller(); got != c.want {
			t.Errorf("%s=%s: %q, want %q", c.env, c.value, got, c.want)
		}
	}
	// A shell that declares a kind it can't prove is other.
	asOperator(t)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv(browserctl.EnvAgentKind, browserctl.KindCoder)
	if got := Caller(); got != "agent:other" {
		t.Errorf("an unproven coder shell: %q", got)
	}
	browserctltest.AsAgentKind(t, browserctl.KindCoder)
	if got := Caller(); got != "agent:coder" {
		t.Errorf("a coder chat's shell: %q", got)
	}
}

// A run an AI started, the org side's and any run in a process that is not
// the operator's are not the operator's; the user's own runs are.
func TestAIRun(t *testing.T) {
	asOperator(t)
	ctx := context.Background()
	if AIRun(ctx) || AIRun(workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{"who": "me"})) {
		t.Fatal("the user's own run")
	}
	for name, run := range map[string]context.Context{
		"a chat's mark":   workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "chat"}),
		"an agent's mark": workflow.WithTrigger(ctx, "trigger.manual", map[string]interface{}{workflow.AICallerKey: "other/org"}),
		"an org's call":   workflow.WithTrigger(ctx, workflow.TriggerTypeOrgTool, nil),
		"trigger.org":     workflow.WithTrigger(ctx, workflow.TriggerNodeTypeOrg, nil),
	} {
		if !AIRun(run) {
			t.Errorf("%s is the operator's", name)
		}
	}
	if !AIRun(workflow.WithAIAuthored(ctx)) {
		t.Error("a run of a definition the operator never approved is the operator's")
	}
	t.Setenv(workflow.EnvWorkflowRun, "exec-7")
	if !AIRun(ctx) {
		t.Error("a run in a process a workflow node started is the operator's")
	}
}

type approvals struct{ ids []string }

func (a *approvals) ApproveDefinition(_ context.Context, id string) error {
	a.ids = append(a.ids, id)
	return nil
}

// A refusal says how to fix a marker a long-lived process inherited: the
// app, the daemon or the bridge started from a marked shell refuses the
// user's own actions until it is started again from their own terminal.
func TestRefusalSaysHowToFixAnInheritedMarker(t *testing.T) {
	for _, k := range []string{workflow.EnvWorkflowRun, browserctl.EnvAICaller, "CLAUDECODE"} {
		asOperator(t)
		t.Setenv(k, "x")
		r := Refusal()
		for _, want := range []string{k + " is set", "the MonoAgent app, the daemon or the browser bridge", "inherited this marker",
			"restart it from your own terminal or the desktop launcher", "`systemctl --user restart monoagent-bridge`"} {
			if !strings.Contains(r, want) {
				t.Errorf("%s: %q lacks %q", k, r, want)
			}
		}
	}
	if h := InheritedHint(); !strings.HasPrefix(h, "If you started the MonoAgent app") || !strings.HasSuffix(h, "for the daemon).") {
		t.Errorf("InheritedHint() = %q", h)
	}
}

// The turn check the command line installs makes a process with no marker stop approving; a process
// that installed none is judged by marker alone, and an agent's marker still decides first.
func TestApprovingContextConsultsTheInstalledTurnCheck(t *testing.T) {
	asOperator(t)
	t.Cleanup(func() { SetTurnVerdict(nil) })
	ctx := context.Background()
	approves := func() bool { return workflowApproves(ApprovingContext(ctx)) }

	SetTurnVerdict(nil)
	if !approves() || NotApproving() {
		t.Fatal("the operator with no turn check does not approve")
	}
	SetTurnVerdict(func() string { return "" })
	if !approves() {
		t.Fatal("a turn check that finds no turn stopped the operator from approving")
	}
	SetTurnVerdict(func() string { return "inside a turn" })
	if approves() || !NotApproving() || InTurn() == "" {
		t.Fatal("a process inside a turn approves")
	}
	actx := ApprovingContext(ctx)
	if n := UnapprovedNote(actx, "saved", "wf-9"); !strings.Contains(n, "NOT approved") || !strings.Contains(n, "workflow activate wf-9") || !strings.Contains(n, "inside a turn") {
		t.Fatalf("note = %q", n)
	}
	t.Setenv(hilsend.EnvAgentContext, "coder")
	if approves() || UnapprovedNote(ApprovingContext(ctx), "saved", "wf-9") != "" || CannotApproveNote(ApprovingContext(ctx), "wf-9") != "" {
		t.Fatal("a marked process approves, or its output gained a note")
	}
	SetTurnVerdict(nil)
	if approves() {
		t.Fatal("a marked process approves")
	}
}

// workflowApproves reports whether ApprovingContext decided to approve (the cmd/monoagentcli tests read
// the approval row itself).
func workflowApproves(ctx context.Context) bool { return Approves(ctx) }

// The verdict is asked once per command and the note reads that answer: a verdict that flips between
// two asks (a transient lock) can not make the note disagree with what was written.
func TestTheNoteReadsTheVerdictTheWriteUsed(t *testing.T) {
	asOperator(t)
	t.Cleanup(func() { SetTurnVerdict(nil) })
	for name, answers := range map[string][]string{"clear then turn": {"", "in a turn"}, "turn then clear": {"in a turn", ""}} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			SetTurnVerdict(func() string { a := answers[min(calls, len(answers)-1)]; calls++; return a })
			actx := ApprovingContext(context.Background())
			wrote := workflowApproves(actx)
			noted := UnapprovedNote(actx, "saved", "wf-1") != ""
			cannot := CannotApproveNote(actx, "wf-1") != ""
			if wrote == noted || wrote == cannot {
				t.Fatalf("approval written = %v, note = %v, up-front note = %v", wrote, noted, cannot)
			}
			if wrote != (answers[0] == "") {
				t.Fatalf("the write did not use the first answer")
			}
			ApprovingContext(actx) // a second use of the same context does not ask again
			if calls != 1 {
				t.Fatalf("the verdict was asked %d times", calls)
			}
		})
	}
}

// The note says the real cause (the turn check's own text), and for a process the check could not place
// it says to retry, not to approve elsewhere.
func TestTheNoteNamesTheCause(t *testing.T) {
	asOperator(t)
	t.Cleanup(func() { SetTurnVerdict(nil) })
	for _, c := range []struct{ why, want, notWant string }{
		{"MONOAGENT_SESSION_TOKEN is set, so a process holding an agent token is running this command", "agent token", "wait and retry"},
		{"this command runs inside a coder session's turn, so a model is running it", "coder session's turn", "wait and retry"},
		{UnplacedPrefix + " (database is locked)", "wait and retry", "workflow activate wf-2` in your own terminal"},
	} {
		SetTurnVerdict(func() string { return c.why })
		n := UnapprovedNote(ApprovingContext(context.Background()), "saved", "wf-2")
		if !strings.Contains(n, c.want) || strings.Contains(n, c.notWant) {
			t.Errorf("%q: note %q", c.why, n)
		}
	}
}

func TestWithNoteLeavesTheOperatorsResultAlone(t *testing.T) {
	in := map[string]any{"id": "a"}
	if got := WithNote(in, ""); got == nil || len(got.(map[string]any)) != 1 {
		t.Fatalf("got %v", got)
	}
	m := WithNote(struct {
		ID string `json:"id"`
	}{"a"}, "n").(map[string]json.RawMessage)
	if string(m["approved"]) != "false" || string(m["approval_note"]) != `"n"` || string(m["id"]) != `"a"` {
		t.Fatalf("got %v", m)
	}
}
