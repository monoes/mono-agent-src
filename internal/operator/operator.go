// Package operator tells the operator's own terminal from everything else
// that runs mono-agent's CLI: an agent, a process of any kind of agent, the
// chat's runs and whatever a workflow node starts. What only the operator
// may decide (approvals, browser permissions, org signatures, an org role's
// full access) is refused unless Marker is "".
//
// The check reads marks, not identity. A process is the operator's when its
// own environment carries none of the variables agents, the chat's runs and
// workflow nodes are started with, and, on Linux, none of its parent
// processes (up to ancestorMaxDepth of them, 32) started with any variable
// MarkerEnv names (MONOAGENT_WORKFLOW_RUN, MONOAGENT_AI_CALLER, CLAUDECODE,
// MONOAGENT_ACTOR and every other agent's marker, generic ones such as AGENT,
// AI_AGENT, CRUSH and OPENCODE included) or is a `monomind agent exec`. A
// chain of parents that goes on past that depth counts as marked
// (ancestorTooDeep): nesting clean shells is how a marked caller would put
// its mark out of reach. So a process that
// clears its own environment still counts as an agent's while its parents
// show where it came from; one that also leaves that line of parents (a
// double fork that hands it to init, a job given to systemd or cron, a
// process another user's account starts) counts as the operator, and on
// macOS and Windows, where there is no parent check, clearing the
// environment is enough. An agent with a shell can also edit the database or
// mono-agent's files directly. This keeps honest agents and prompt-injected
// ones that only run commands away from the operator's decisions; it is a
// courtesy layer, not a sandbox.
package operator

import (
	"context"
	"os"
	"strings"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Marker names the environment variable that makes this process not the
// operator's own terminal, or "" for the operator: a process a workflow node
// started (workflow.EnvWorkflowRun, which a chat or an org can have built),
// a chat tool call's run (browserctl.EnvAICaller), an agent (hilsend.Detect)
// or any kind of agent (browserctl.AgentKind); else what marks one of its
// parent processes (ancestorMarker: "a parent process's …").
func Marker() string {
	for _, k := range []string{workflow.EnvWorkflowRun, browserctl.EnvAICaller} {
		if os.Getenv(k) != "" {
			return k
		}
	}
	if _, agent := hilsend.Detect(); !agent && browserctl.AgentKind() == "" {
		return ancestorMarker()
	}
	if m := orgsign.AgentContextMarker(); m != "" {
		return m
	}
	for _, k := range []string{hilsend.EnvAgentContext, "MONOAGENT_ACTOR", browserctl.EnvAgentKind} {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return "an agent marker"
}

// MarkerEnv is every environment variable Marker reads (with
// browserctl.AgentKind behind it): what a process must not carry for its
// children to count as the operator's. The extension bridge removes exactly
// these from the side panel's coder turns (internal/extension, a test holds
// its copy equal).
func MarkerEnv() []string {
	return append(browserctl.AgentContextEnv(), workflow.EnvWorkflowRun, browserctl.EnvAICaller,
		browserctl.EnvAgentKind, browserctl.EnvAgentKindToken)
}

// Caller names what runs this process when it is not the operator's own
// terminal, for what it leaves on record (the author of a task it adds):
// "workflow:<execution id>" for a process a workflow node started, "chat" for
// the chat's runs, "agent:<kind>" for an agent (browserctl.AgentKind: a kind it
// could not prove is other); "" for the operator.
func Caller() string {
	switch m := Marker(); m {
	case "":
		return ""
	case workflow.EnvWorkflowRun:
		return "workflow:" + strings.TrimSpace(os.Getenv(m))
	case browserctl.EnvAICaller:
		if strings.TrimSpace(os.Getenv(m)) == browserctl.KindChat {
			return browserctl.KindChat
		}
	}
	kind := browserctl.AgentKind()
	if kind == "" || kind == browserctl.KindChat {
		kind = browserctl.KindOther
	}
	return "agent:" + kind
}

// AIRun reports whether the workflow run in ctx is not the operator's: this
// process is not the operator's own terminal (Marker), an AI started the run
// (workflow.AICallerKey, the mark a chat's or an agent's `workflow run`
// leaves for the engine that runs it: this one, or a daemon that adopts it),
// the org side did (a granted call, a message to an automation role, a
// trigger.org event), or the workflow's definition is one the operator never
// approved (workflow.AIAuthoredFrom: the chat, an agent or a workflow made or
// changed it since). Any other run the user starts from the app, a terminal,
// a schedule or a webhook is the operator's.
func AIRun(ctx context.Context) bool {
	if Marker() != "" || workflow.AIAuthoredFrom(ctx) {
		return true
	}
	if v, _ := workflow.TriggerDataFrom(ctx)[workflow.AICallerKey].(string); strings.TrimSpace(v) != "" {
		return true
	}
	switch workflow.TriggerTypeFrom(ctx) {
	case workflow.TriggerTypeOrgTool, workflow.TriggerTypeOrgMessage, workflow.TriggerNodeTypeOrg:
		return true
	}
	return false
}

// inheritedHint says how to fix a refusal the user meets on their own
// action. A long-lived process started from a marked one (an agent's shell,
// a chat's or a workflow's run) inherits its marker, and so does everything
// it starts: the user's own actions through it are refused too. That fails
// closed on purpose; clearing the marker in the process would let an
// agent-started daemon's runs pass for the user's.
const inheritedHint = "if you started the MonoAgent app, the daemon or the browser bridge from an agent shell, a chat or a workflow run, " +
	"it inherited this marker: restart it from your own terminal or the desktop launcher " +
	"(`systemctl --user restart monoagent-bridge` for the bridge service, `systemctl --user restart monoagent-daemon` for the daemon)"

// InheritedHint is inheritedHint as a sentence of its own, for a refusal
// that does not go through Refusal.
func InheritedHint() string { return "If" + strings.TrimPrefix(inheritedHint, "if") + "." }

// InheritedHintClause is inheritedHint as a clause, for a refusal that gives
// it in parentheses after its reason, as Refusal does.
func InheritedHintClause() string { return inheritedHint }

// Refusal says why this process is not the operator's own terminal, and how
// to fix a marker it inherited, for an error message; "" for the operator.
func Refusal() string {
	why := ""
	switch m := Marker(); m {
	case "":
		return ""
	case workflow.EnvWorkflowRun:
		why = m + " is set, so a workflow run is running this command"
	case browserctl.EnvAICaller:
		why = m + " is set, so the chat is running this command"
	case ancestorTooDeep:
		why = "it runs under " + m + " processes, which can hide an agent, the chat or a workflow run that started it, so it counts as theirs"
	default:
		if strings.HasPrefix(m, ancestorPrefix) {
			why = m + " marks it, so a workflow run, the chat or an agent started this command, whatever its own environment says"
			break
		}
		why = m + " is set, so an agent is running this command"
	}
	return why + " (" + inheritedHint + ")"
}

// ApprovingContext makes the publication that is made with the returned context also record its
// resulting definition as the operator's, in the same commit as the rows (unless an agent is
// running this command). It is the only way an operator command approves a workflow: the approval
// is written with the change that makes the definition (a save, or the activation), never in a step
// after it, so it cannot cover a change made in between.
func ApprovingContext(ctx context.Context) context.Context {
	if Marker() != "" {
		return ctx
	}
	return workflow.WithApproval(ctx)
}
