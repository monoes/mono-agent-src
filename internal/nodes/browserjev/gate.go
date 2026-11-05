package browserjev

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgsign"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

// EnvAICaller marks a process whose workflow run an AI caller started
// without the process being an agent's own: the chat's run_workflow tool
// sets it to "chat" on the `workflow run` it starts (internal/ai/chat).
const EnvAICaller = "MONOAGENT_AI_CALLER"

// caller is who started a run when it was not the user.
type caller struct {
	name string             // for the run's errors: "chat", "an agent (CLAUDECODE)", "org growth"
	by   hilsend.PreparedBy // for the approval cards; System only for the engine's own marks
	// kind is the kind of agent (browserctl.Kind*), whose level in the
	// user's browser permissions the run follows.
	kind string
	// declared is a kind the agent claimed but could not prove
	// (browserctl.Caller): its level binds too when it is stricter.
	declared string
	// authored: no AI started the run, but its workflow is one the user
	// never approved (workflow.AIAuthoredFrom). Who wrote it isn't
	// recorded, so policyFor takes the stricter of the chat's and other
	// agents' levels, and kind becomes that one.
	authored bool
}

// aiCaller reports who started this run when it was not the user, and its
// kind: an agent's process (a coder chat or its worker, an org role's shell,
// any other agent shell or MCP server: browserctl.AgentKind), a chat tool
// call (EnvAICaller: chat), a run either of them started with `workflow run`
// (workflow.AICallerKey, the kind it was marked with), or the org side (a
// granted call, a message to an automation role, a trigger.org event: org).
// A run the user started from the app, a terminal, a schedule or a webhook
// is none of these, unless the workflow is one the user never approved
// (workflow.AIAuthoredFrom: the chat, an agent or another workflow wrote or
// changed it since): then an AI wrote what runs, and it is held like one.
func aiCaller(ctx context.Context) (caller, bool) {
	if c, ok := envCaller(); ok {
		return c, true
	}
	// A run this process didn't start: the AI caller's `workflow run` marked
	// it with its kind (MarkAICaller); an unknown mark ("agent") is other.
	if v, _ := workflow.TriggerDataFrom(ctx)[workflow.AICallerKey].(string); strings.TrimSpace(v) != "" {
		kind, declared, _ := strings.Cut(v, "/")
		if declared != browserctl.KindCoder && declared != browserctl.KindOrg {
			declared = ""
		}
		switch kind {
		case workflow.UnapprovedWorkflowCaller:
			// A workflow called by one the user never approved: held as that
			// workflow is (the stricter of chat's and other's levels).
			return caller{name: "a workflow the user hasn't approved", by: hilsend.PreparedBy{AgentID: workflow.UnapprovedWorkflowCaller, System: true},
				kind: browserctl.KindOther, authored: true}, true
		case browserctl.KindChat:
			return caller{name: "chat", by: hilsend.PreparedBy{AgentID: kind}, kind: kind, declared: declared}, true
		case browserctl.KindCoder, browserctl.KindOrg:
			return caller{name: "an agent", by: hilsend.PreparedBy{AgentID: kind}, kind: kind, declared: declared}, true
		}
		return caller{name: "an agent", by: hilsend.PreparedBy{AgentID: browserctl.KindOther}, kind: browserctl.KindOther, declared: declared}, true
	}
	switch workflow.TriggerTypeFrom(ctx) {
	case workflow.TriggerTypeOrgTool, workflow.TriggerTypeOrgMessage, workflow.TriggerNodeTypeOrg:
		// The card shows the role as the org side's claim: a granted call is
		// the role whose grant id the caller used, which only the caller's
		// environment vouches for (mcp grantScope).
		src := workflow.PublicationSource(ctx, workflow.NodeInput{})
		return caller{name: strings.TrimSpace("org " + src.OrgID),
			by: hilsend.PreparedBy{Org: src.OrgID, Role: src.RoleID, AgentID: src.AgentID}, kind: browserctl.KindOrg}, true
	}
	if workflow.AIAuthoredFrom(ctx) {
		return caller{name: "a workflow the user hasn't approved", by: hilsend.PreparedBy{AgentID: "unapproved-workflow", System: true},
			kind: browserctl.KindOther, authored: true}, true
	}
	return caller{}, false
}

// envCaller is an AI caller this process's environment names. The agent
// markers come first: a shell can't pass itself off as the chat by setting
// EnvAICaller.
func envCaller() (caller, bool) {
	if browserctl.IsAgentContext() {
		name := "an agent"
		if m := orgsign.AgentContextMarker(); m != "" {
			name += " (" + m + ")"
		}
		c := browserctl.MarkerCaller()
		return caller{name: name, by: browserctl.RequestedBy(), kind: c.Kind, declared: c.Declared}, true
	}
	if v := strings.TrimSpace(os.Getenv(EnvAICaller)); v != "" {
		kind := browserctl.KindChat
		if v != browserctl.KindChat {
			kind = browserctl.KindOther
		}
		return caller{name: v, by: hilsend.PreparedBy{AgentID: v}, kind: kind}, true
	}
	return caller{}, false
}

// MarkAICaller marks a run's trigger data with the kind of the AI caller
// that starts it from this process (`workflow run`), so the engine that runs
// it — this one, or a daemon that adopts a --no-wait run or resumes a paused
// one — holds it to the same level. A kind the caller declared but could
// not prove follows as "kind/declared" ("other/org").
func MarkAICaller(data map[string]interface{}) {
	MarkAICallerFor(context.Background(), nil, data, "", false)
}

// MarkAICallerRun is MarkAICaller with the opener of the database a run's token is looked up in.
func MarkAICallerRun(ctx context.Context, open func() (*sql.DB, error), data map[string]interface{}) {
	MarkAICallerFor(ctx, open, data, "", false)
}

// MarkAICallerFor is MarkAICaller for a command that also knows, from its own verdict (internal/cli/opgate.Refusal:
// markers, agent tokens, the ancestry of a coder turn), that its process is not the user's terminal: why says so.
// A marker still names its own kind, except a bare MONOAGENT_AI_CALLER=chat when inTurn (the process descends from
// a recorded coder turn): no real agent marker backs it, so the stricter of chat and other applies.
// A process a workflow node started (or one under such a process) that cannot show the token of an approved, live
// run (operator.UnapprovedRunProcess; open opens the profile's database, called only when something marks the
// process; no opener cannot show one) marks its run as called by an unapproved workflow. Only after that, a
// process that shows no marker but whose command knows it is not the user's terminal (why) is marked as another
// agent's (a coder turn clears its markers; the marker alone said "the user").
func MarkAICallerFor(ctx context.Context, open func() (*sql.DB, error), data map[string]interface{}, why string, inTurn bool) {
	if data == nil {
		return
	}
	if c, ok := envCaller(); ok {
		mark := c.kind
		if mark == browserctl.KindChat && inTurn {
			mark = browserctl.KindOther
		}
		if c.declared != "" {
			mark += "/" + c.declared
		}
		data[workflow.AICallerKey] = mark
		return
	}
	// A command a workflow node started in a run nobody approved (or one such a run called) that
	// starts another run: that run is called by an unapproved workflow, whatever its own markers.
	if operator.UnapprovedRunProcess(ctx, open) {
		data[workflow.AICallerKey] = workflow.UnapprovedWorkflowCaller
		return
	}
	if why != "" {
		data[workflow.AICallerKey] = browserctl.KindOther
		return
	}
}

// policyFor is the policy the user set for who's kind of agent (Settings →
// Agent browser permissions), with the site rules; for an authored run, the
// stricter of the chat's and other agents' (Policy.Kind says which). Without
// a database the defaults apply.
func policyFor(ctx context.Context, who caller) (browserctl.Policy, error) {
	p := browserctl.DefaultPermissions()
	if db := vault.DBFromContext(ctx); db != nil {
		var err error
		if p, err = browserctl.LoadPermissions(db); err != nil {
			return browserctl.Policy{}, fmt.Errorf("read the browser permissions: %w", err)
		}
	}
	if who.authored {
		who.kind = p.StricterKind(browserctl.KindOther, browserctl.KindChat)
	}
	return p.PolicyForCaller(browserctl.Caller{Kind: who.kind, Declared: who.declared}), nil
}

// blockedSubmit is one submit the guard stopped (C1 submit_guard's blocked[]).
type blockedSubmit struct {
	Seq     int
	URL     string
	FrameID int                    // 0: the top frame
	Form    browserctl.FormSummary // sensitive values masked
}

// submitGuard is the submit guard armed on the run's tab and the approval
// behind it (gate_ext.go).
type submitGuard interface {
	// Blocked returns the submits it blocked since the last call.
	Blocked(ctx context.Context) ([]blockedSubmit, error)
	// Ask makes the browser.submit Human-in-Loop item for b.
	Ask(ctx context.Context, b blockedSubmit, by hilsend.PreparedBy) (hilID string, err error)
	// Decision is the item's status: pending, approved, rejected,
	// consumed (`browser submit --hil` already sent it), or
	// approved_elsewhere (not by the user, so it can't be used).
	Decision(ctx context.Context, hilID string) (string, error)
	// Submit lets b through, exactly once, then the guard holds again; with
	// hilID, the approved item is used up by it.
	Submit(ctx context.Context, b blockedSubmit, hilID string) error
	// Follow re-arms the guard when pageURL is on another origin than the
	// one it was armed on: the extension disarms a tab whose top page moves
	// to another origin, so a submit there would go unguarded until the next
	// Blocked.
	Follow(ctx context.Context, pageURL string) error
	// TabID is the tab it guards (0 when the page can't say).
	TabID() int
}

// guardable is a driver that can arm the submit guard on its tab.
type guardable interface {
	ArmSubmitGuard(ctx context.Context) (submitGuard, error)
}

// approvalPoll is how often a waiting run reads its item's decision.
var approvalPoll = 2 * time.Second

// guardSettle is how long a submit an action set off takes to reach the
// guard's record (the page posts it to the extension).
var guardSettle = 300 * time.Millisecond

func waitForGuard(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(guardSettle):
	}
}

// gateEnd is how the gate ended a run: its status and reason, and the item.
type gateEnd struct {
	status, reason, hilID string
}

// settleSubmits handles what the guard blocked during the last action. A
// submit on a local or dev page goes through (an allowlisted real site
// still asks: the allowlist says where an agent may go, not what it may
// send). One on a real site becomes a browser.submit item and the run
// waits for the user: approved, it goes through and the run goes on (nil);
// rejected, the run ends blocked; still pending when the run's time is up,
// it ends awaiting_approval and the tab stays open, so `browser submit
// --hil <id>` can still send it.
func settleSubmits(ctx context.Context, g submitGuard, c caller) (*gateEnd, error) {
	blocked, err := g.Blocked(ctx)
	if err != nil || len(blocked) == 0 {
		return nil, err
	}
	b := blocked[len(blocked)-1] // one action, one submit: the latest form state
	origin := browserctl.Origin(b.URL)
	if browserctl.IsLocal(b.URL) {
		if err := g.Submit(ctx, b, ""); errors.Is(err, browserctl.ErrNotSent) {
			return &gateEnd{"blocked", fmt.Sprintf("the form submit on %s was held and could not be repeated: %v", origin, err), ""}, nil
		} else if err != nil {
			return nil, err
		}
		return nil, nil
	}
	id, err := g.Ask(ctx, b, c.by)
	if err != nil {
		return nil, fmt.Errorf("ask for approval of the submit on %s: %w", origin, err)
	}
	for {
		status, err := g.Decision(ctx, id)
		if err != nil && ctx.Err() == nil {
			return nil, err
		}
		switch status {
		case "approved":
			// Nothing reached the page (it changed since the approval, the
			// form is in a frame, …): the approval was handed back.
			if err := g.Submit(ctx, b, id); errors.Is(err, browserctl.ErrNotSent) {
				return &gateEnd{"blocked", fmt.Sprintf("%v (Human-in-Loop item %s, handed back)", err, id), id}, nil
			} else if err != nil {
				return nil, err
			}
			return nil, nil
		case "consumed":
			return nil, nil
		case "rejected":
			return &gateEnd{"blocked", fmt.Sprintf("the user rejected the form submit on %s (Human-in-Loop item %s)", origin, id), id}, nil
		case "approved_elsewhere":
			return &gateEnd{"blocked", fmt.Sprintf("the form submit on %s (Human-in-Loop item %s) was approved outside "+
				"`monoagentcli hil approve` and the Human in Loop page, so it was not sent", origin, id), id}, nil
		}
		select {
		case <-ctx.Done():
			return &gateEnd{"awaiting_approval", fmt.Sprintf("the form submit on %s waits for the user's approval (Human-in-Loop item %s); "+
				"once approved, `monoagentcli browser submit --hil %s` sends it from the open tab", origin, id, id), id}, nil
		case <-time.After(approvalPoll):
		}
	}
}
