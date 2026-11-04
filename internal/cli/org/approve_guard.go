package org

import (
	"context"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/orgdecide"
)

// refuseSendingApproval refuses `org approve <org> <role> <action>` to what
// is not the operator's own terminal (opgate: an agent, the chat, a workflow,
// a process inside a coder session's turn) unless one of org's pending approvals is exactly role's
// action, and approving its stored action lets no message to a person go out
// (refuseSendingAction). A typed variant of a pending action (another case,
// spaces) matches none, so it is refused even if monomind would take it.
// The operator approves anything, as before.
func (env *orgEnv) refuseSendingApproval(ctx context.Context, org, role, action string) error {
	why := opgate.Refusal()
	if why == "" {
		return nil
	}
	raw, err := monomind.OrgApprovals(ctx, env.root(), org)
	var items []orgdecide.Item
	if err == nil {
		items, err = orgdecide.ParseApprovals(raw)
	}
	if err != nil {
		return env.d.InvalidInput("approving %s can't be checked against org %s's pending approvals (%v): %s; deny it, or approve it yourself from a terminal or the app", action, org, err, why)
	}
	for _, it := range items {
		if it.Requester == role && it.Action == action {
			return env.refuseSendingAction(ctx, org, it.Action, why)
		}
	}
	return env.d.InvalidInput("org %s has no pending approval of exactly %q by %q: %s; see them with `monoagentcli org approvals %s`, or approve it yourself from a terminal or the app", org, action, role, why, org)
}

// refuseChatApproval is refuseSendingApproval for `org chat approve`, which
// found the pending approval itself: action is its stored action.
func (env *orgEnv) refuseChatApproval(ctx context.Context, org, action string) error {
	why := opgate.Refusal()
	if why == "" {
		return nil
	}
	return env.refuseSendingAction(ctx, org, action, why)
}

// refuseSendingAction refuses approving an approval of org for action, a
// pending approval's stored action, when that lets a message to a person go
// out (a call of a granted automation whose workflow may send:
// orgdecide.ItemSends): only the user approves that, and why says what this
// process is instead. Denying it stays anyone's, and approvals of other
// actions pass. When whether it sends can't be told, it is refused.
func (env *orgEnv) refuseSendingAction(ctx context.Context, org, action, why string) error {
	it := orgdecide.Item{Kind: orgdecide.KindApproval, Class: orgdecide.ClassForAction(action), Action: action}
	if !strings.HasPrefix(it.Class, "grant:") {
		return nil
	}
	db, profileID, _, err := env.Profile()
	if err != nil {
		return env.d.InvalidInput("approving %s may let a message to a person go out, and that can't be checked (%v): %s; deny it, or approve it yourself from a terminal or the app", action, err, why)
	}
	if orgdecide.ItemSends(ctx, db.DB, profileID, org, it, orgenv.GrantWorkflows(db, env.OpenWorkflowStore)) {
		return env.d.InvalidInput("approving %s lets a message to a person go out, which only the user approves: %s; deny it, or approve it yourself from a terminal or the app", action, why)
	}
	return nil
}
