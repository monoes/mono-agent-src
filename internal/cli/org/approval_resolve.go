package org

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/monomind"
)

type resolveApprovalFunc func(ctx context.Context, root, org, role, action string, opts monomind.ResolveOptions) (json.RawMessage, error)

func newOrgApproveCmd(env *orgEnv) *cobra.Command {
	return newOrgApprovalCmd(env, "approve", "Approve a pending tool-approval request", env.refuseSendingApproval, monomind.OrgApproveWith)
}

func newOrgDenyCmd(env *orgEnv) *cobra.Command {
	return newOrgApprovalCmd(env, "deny", "Deny a pending tool-approval request", nil, monomind.OrgDenyWith)
}

// newOrgApprovalCmd is `org approve|deny`. With --request it resolves that one
// request (`org approvals` names them), which is what the app does with the
// request its card showed; without it every pending request of (role, action),
// including one queued since the caller looked. refuse, when set, is asked
// first.
func newOrgApprovalCmd(env *orgEnv, verb, short string, refuse func(ctx context.Context, org, role, action string) error, resolve resolveApprovalFunc) *cobra.Command {
	var request string
	c := &cobra.Command{
		Use:   verb + " <name> <role> <action>",
		Short: short,
		Long: short + ". With --request <id> (a requestId of `org approvals`) only that request is resolved; " +
			"it is refused when it is no longer pending or monomind cannot resolve by id. Without it every " +
			"pending request of the role and action is resolved, also one queued after you looked.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if refuse != nil {
				if err := refuse(cmd.Context(), args[0], args[1], args[2]); err != nil {
					return err
				}
			}
			// An empty or odd id must not fall back to resolving every request.
			if cmd.Flags().Changed("request") && !orgChatRef.MatchString(request) {
				return env.d.InvalidInput("invalid approval request id %q: nothing was sent", request)
			}
			out, err := resolve(cmd.Context(), env.root(), args[0], args[1], args[2], monomind.ResolveOptions{RequestID: request})
			if err != nil {
				return err
			}
			return printOrgJSON(out)
		},
	}
	c.Flags().StringVar(&request, "request", "", "Resolve only this approval request (a requestId from org approvals)")
	return c
}
