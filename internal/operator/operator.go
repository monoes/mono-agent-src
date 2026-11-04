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
	"encoding/json"
	"fmt"
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

// turnVerdict says why this process runs inside a coder session's turn ("" when it does not). Only
// a process that registered the turn-aware gate (internal/cli/opgate.Register, the command line)
// has one; every other process (the app, the daemon, MCP servers, library use) judges by Marker alone.
var turnVerdict func() string

// SetTurnVerdict installs the turn check ApprovingContext and NotApproving consult after Marker;
// nil removes it. It is called from opgate only, never at package init: the check may open the
// database, and operator must stay free of it.
func SetTurnVerdict(f func() string) { turnVerdict = f }

// InTurn says why this process counts as a coder turn's although it carries no marker (a turn whose
// whole process chain was stripped of its markers): "" when it does not, or when no turn check is
// installed.
func InTurn() string {
	if turnVerdict == nil {
		return ""
	}
	return turnVerdict()
}

// NotUser says why this process is not the user's own terminal, as the turn-aware gate
// (internal/cli/opgate.Refusal, non-recovery flavour) would, for a package that cannot import opgate:
// Refusal's text for a marked process, else InTurn's. "" for the user. A process that registered no
// turn check (the app, the daemon, library use) is judged by its marker alone. A test in
// cmd/monoagentcli holds it to opgate.Refusal's verdict in every context.
func NotUser() string { return askVerdict().why }

// UnplacedPrefix starts the verdict text of a process the turn check could not place (the database or
// a turn-registry entry could not be read): the one cause whose fix is a retry, not an approval.
const UnplacedPrefix = "cannot tell whether a coder session's turn runs this command"

// verdict is the answer to "may this process approve?", asked once per command and carried in the
// context ApprovingContext returns, so the write and the note read the same answer.
type verdict struct {
	why    string // "" approves; else the cause: Refusal's text for a marker, the turn check's own text otherwise
	marked bool   // a marker names the process: its output stays as it was, no note
}

type verdictKey struct{}

func verdictIn(ctx context.Context) *verdict {
	v, _ := ctx.Value(verdictKey{}).(*verdict)
	return v
}

func askVerdict() *verdict {
	if Marker() != "" {
		return &verdict{why: Refusal(), marked: true}
	}
	return &verdict{why: InTurn()}
}

// NotApproving reports whether a publication made by this process must not record the operator's
// approval: an agent runs it (Marker) or it runs inside a coder turn (InTurn). A person at their own
// terminal is the only process that approves.
func NotApproving() bool { return askVerdict().why != "" }

// ApprovingContext makes the publication that is made with the returned context also record its
// resulting definition as the operator's, in the same commit as the rows (unless an agent is
// running this command, or this process runs inside a coder turn: NotApproving). It is the only way
// an operator command approves a workflow: the approval is written with the change that makes the
// definition (a save, or the activation), never in a step after it, so it cannot cover a change made
// in between. The verdict is asked once and rides in the returned context: pass that context to
// NoteUnapproved and CannotApproveNote so a note never disagrees with what was written.
func ApprovingContext(ctx context.Context) context.Context {
	v := verdictIn(ctx)
	if v == nil {
		v = askVerdict()
		ctx = context.WithValue(ctx, verdictKey{}, v)
	}
	if v.why != "" {
		return ctx
	}
	return workflow.WithApproval(ctx)
}

// Approves reports whether ctx, as ApprovingContext returned it, approves the publication made with it.
func Approves(ctx context.Context) bool {
	v := verdictIn(ctx)
	return v != nil && v.why == ""
}

// cause says why this process did not approve and how the user fixes it, from the verdict text itself.
func (v *verdict) cause(id string) string {
	if strings.HasPrefix(v.why, UnplacedPrefix) {
		return v.why + ". The database or the turn registry could not be read, so nothing was approved: wait and retry, " +
			"or fix the problem the message names (`monoagentcli doctor` lists it); approving from another terminal is refused for the same reason until it reads"
	}
	return v.why + ". To approve it, run `monoagentcli workflow activate " + id + "` in your own terminal, or approve it in the app"
}

// UnapprovedNote is the line a command prints when it saved a workflow without approving it
// because the process is not the operator's own terminal; ctx is what ApprovingContext returned.
// "" for the operator, for a marked process (whose output stays as it was) and for a ctx that did
// not come from ApprovingContext. what is "saved", "imported" and so on.
func UnapprovedNote(ctx context.Context, what, id string) string {
	v := verdictIn(ctx)
	if v == nil || v.marked || v.why == "" {
		return ""
	}
	return "this workflow was " + what + " but NOT approved: " + v.cause(id) + "."
}

// CannotApproveNote is said up front by a command that needs the operator's approval to go on: an
// activation (which only succeeds for a definition already approved) or, with newWorkflow, a template
// run (whose temporary workflow is new, so it has no approval and the run is refused). "" when this
// process approves or is marked.
func CannotApproveNote(ctx context.Context, id string, newWorkflow bool) string {
	v := verdictIn(ctx)
	if v == nil || v.marked || v.why == "" {
		return ""
	}
	tail := "Activation only succeeds for a definition you already approved."
	if newWorkflow {
		tail = "The temporary workflow this run creates is new and cannot be approved here, so the run will be refused."
	}
	return "this process cannot approve a workflow: " + v.cause(id) + ". " + tail
}

// NoteUnapproved prints the UnapprovedNote on stderr (nothing for the operator and for a marked
// process), and returns it for the --json result.
func NoteUnapproved(ctx context.Context, what, id string) string {
	note := UnapprovedNote(ctx, what, id)
	if note != "" {
		fmt.Fprintln(os.Stderr, "Note: "+note)
	}
	return note
}

// WithNote is v, or, when note is not empty, v with "approved": false and "approval_note": note added to
// its JSON object. The operator's and a marked process's result is v itself, byte for byte.
func WithNote(v any, note string) any {
	if note == "" {
		return v
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return v
	}
	m["approved"] = json.RawMessage("false")
	n, _ := json.Marshal(note)
	m["approval_note"] = n
	return m
}
