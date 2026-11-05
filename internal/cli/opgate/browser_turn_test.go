package opgate_test

import (
	"testing"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/workflow"
)

// actAsUnmarkedProcess clears every marker the browser permissions read: the shell of a coder
// turn whose markers were stripped looks like this.
func actAsUnmarkedProcess(t *testing.T) {
	t.Helper()
	for _, k := range append(browserctl.AgentContextEnv(), browserctl.EnvAgentKind, browserctl.EnvAgentKindToken,
		browserctl.EnvAICaller, workflow.EnvWorkflowRun, "MONOAGENT_CONVERSATION_ID", "MONOAGENT_AGENT_ROLE",
		"MONOAGENT_AGENT_ID", "MONOMIND_ORG_NAME", "MONOMIND_ORG_ROLE") {
		t.Setenv(k, "")
	}
}

// A process that descends from a recorded coder turn but carries no marker is an agent for the browser
// permissions: other (never the human's unrestricted level), under every per-command entry.
func TestAnUnmarkedProcessInATurnIsAnAgentForTheBrowserPermissions(t *testing.T) {
	actAsUnmarkedProcess(t)
	opgate.SetForTest(t, func(bool) string { return "this command runs inside a coder session's turn" })
	c := browserctl.AgentCaller()
	if c.Kind != browserctl.KindOther {
		t.Fatalf("AgentCaller = %+v, want kind %q", c, browserctl.KindOther)
	}
	// AgentKind stays marker-only: the operator gate builds on it and must not ask itself.
	if got := browserctl.AgentKind(); got != "" {
		t.Fatalf("AgentKind = %q, want the marker-only answer", got)
	}
	if p := (browserctl.Permissions{}).PolicyForCaller(c); !p.Agent {
		t.Fatalf("policy %+v is not an agent's", p)
	}
	if got := browserctl.ContextKey(); got != "agent" {
		t.Fatalf("ContextKey = %q, want agent (the human's session state is not a turn's)", got)
	}
}

// The user's own terminal outside a turn is unchanged, and so is the chat.
func TestAnUnmarkedProcessOutsideATurnStaysTheHumans(t *testing.T) {
	actAsUnmarkedProcess(t)
	opgate.SetForTest(t, func(bool) string { return "" })
	if c := browserctl.AgentCaller(); c.Kind != "" {
		t.Fatalf("AgentCaller = %+v, want the human", c)
	}
	if got := browserctl.ContextKey(); got != "human" {
		t.Fatalf("ContextKey = %q, want human", got)
	}
}

func TestTheChatKeepsItsKindWhateverTheTurnCheckSays(t *testing.T) {
	actAsUnmarkedProcess(t)
	t.Setenv(browserctl.EnvAICaller, browserctl.KindChat)
	opgate.SetForTest(t, func(bool) string { return "the chat runs this command" })
	if c := browserctl.AgentCaller(); c.Kind != browserctl.KindChat {
		t.Fatalf("AgentCaller = %+v, want chat", c)
	}
}

// With no turn check installed (the app, the daemon, library use) the marker alone decides.
func TestWithoutATurnCheckTheMarkerAloneDecides(t *testing.T) {
	actAsUnmarkedProcess(t)
	opgate.SetForTest(t, nil)
	if c := browserctl.AgentCaller(); c.Kind != "" {
		t.Fatalf("AgentCaller = %+v, want the human", c)
	}
}
