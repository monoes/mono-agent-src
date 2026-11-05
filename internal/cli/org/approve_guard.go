package org

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/cli/orgenv"
	"github.com/monoes/mono-agent/internal/orgdecide"
)

// refuseSendingApproval refuses `org approve <org> <role> <action>` outright to what
// is not the operator's own terminal (opgate: an agent, the chat, a workflow,
// a process inside a coder session's turn): the class-B decision. Denying stays anyone's.
// The operator approves anything, as before.
func (env *orgEnv) refuseSendingApproval(_ context.Context, org, role, action string) error {
	return env.refuseNotOperator(fmt.Sprintf("approving %s by %s in org %s", action, role, org))
}

// refuseChatApproval is refuseSendingApproval for `org chat approve`, which
// found the pending approval itself: action is its stored action.
func (env *orgEnv) refuseChatApproval(ctx context.Context, org, action string) error {
	why := opgate.Refusal()
	if why == "" {
		return nil
	}
	if err := env.refuseSendingAction(ctx, org, action, why); err != nil {
		return err
	}
	// Any other approval is the user's too (class B), as for `org approve`.
	return env.refuseNotOperator(fmt.Sprintf("approving %s in org %s", action, org))
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
