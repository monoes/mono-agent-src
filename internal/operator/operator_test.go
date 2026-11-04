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
	approves := func() bool { return ApprovingContext(ctx) != ctx }

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
	if !strings.Contains(UnapprovedNote("saved", "wf-9"), "NOT approved") || !strings.Contains(UnapprovedNote("saved", "wf-9"), "workflow activate wf-9") {
		t.Fatalf("note = %q", UnapprovedNote("saved", "wf-9"))
	}
	t.Setenv(hilsend.EnvAgentContext, "coder")
	if approves() || UnapprovedNote("saved", "wf-9") != "" {
		t.Fatal("a marked process approves, or its output gained a note")
	}
	SetTurnVerdict(nil)
	if approves() {
		t.Fatal("a marked process approves")
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
