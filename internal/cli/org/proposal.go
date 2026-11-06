package org

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
	"github.com/monoes/mono-agent/internal/storage"
)

// org proposal (#1477): a chat or an agent proposes a grant or an autonomy change and the user confirms it. create,
// list and show are anyone's: a proposal is an inert row, and an agent can do nothing to it afterwards (it cannot
// decide, extend, revive or confirm it; a newer proposal for the same target and conversation label supersedes it, and a label holds
// at most five pending ones per org; conversation_id is a label, never ownership). confirm and decline are the user's: proposal_confirm.go.

func newOrgProposalCmd(env *orgEnv) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proposal",
		Short: "Changes to an org's grants or autonomy that the user confirms",
		Long: "A chat or an agent proposes a grant (grant.add), the revoking of one (grant.remove) or an autonomy change (autonomy.set); nothing changes until the " +
			"user confirms it with `org proposal confirm <id> --hash <hash>`, which runs the real `org grant add` / `org grant remove` / `org autonomy set`. " +
			"A proposal lasts 30 minutes and is used once. Approval none, level full and the autonomy policy cannot be proposed.",
	}
	cmd.AddCommand(newOrgProposalCreateCmd(env), newOrgProposalListCmd(env), newOrgProposalShowCmd(env),
		newOrgProposalConfirmCmd(env), newOrgProposalDeclineCmd(env))
	return cmd
}

// proposalErr gives an error of the store its exit code: 2 for an unknown id, 3 for every other refusal.
func (env *orgEnv) proposalErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, orgproposal.ErrNotFound):
		return env.d.NotFound("%v", err)
	case errors.Is(err, orgproposal.ErrExpired), errors.Is(err, orgproposal.ErrAlreadyDone), errors.Is(err, orgproposal.ErrHashMismatch),
		errors.Is(err, orgproposal.ErrStale), errors.Is(err, orgproposal.ErrTooMany):
		return env.d.InvalidInput("%v", err)
	}
	return err
}

// proposalView is what a proposal prints. The hash is recomputed from the row's content, never the stored column, so
// a row rewritten behind the card shows a hash the old card cannot confirm. current says whether the org is still as
// the proposal found it (pending ones only).
func (env *orgEnv) proposalView(ctx context.Context, db *storage.Database, profileID, root string, p *orgproposal.Proposal) map[string]interface{} {
	var args, result interface{}
	if json.Valid([]byte(p.ArgsJSON)) {
		args = json.RawMessage(p.ArgsJSON)
	}
	if json.Valid([]byte(p.ResultJSON)) {
		result = json.RawMessage(p.ResultJSON)
	}
	var decided, current interface{}
	if p.DecidedAt != nil {
		decided = p.DecidedAt.UTC().Format(time.RFC3339)
	}
	if p.Status == orgproposal.StatusPending {
		doc, _ := orgdesign.Load(root, p.Org)
		plan, _ := env.planProposal(ctx, db, profileID, doc, p.Verb, p.ArgsJSON)
		current = plan != nil && plan.Digest == p.StateDigest
	}
	return map[string]interface{}{
		"id": p.ID, "org": p.Org, "conversation_id": p.ConversationID, "verb": p.Verb, "args": args,
		"preview_text": p.PreviewText, "state_digest": p.StateDigest, "hash": p.RecomputedHash(), "status": p.Status,
		"created_by": p.CreatedBy, "created_at": p.CreatedAt.UTC().Format(time.RFC3339),
		"expires_at": p.ExpiresAt.UTC().Format(time.RFC3339), "decided_at": decided, "result": result, "current": current,
	}
}

// proposedBy is the label of who proposed, taken from the gate's verdict and never consulted by confirm: "operator" only
// when the gate says this is the user's own terminal; otherwise the marker's name for the caller, else "agent:unplaced"
// (an unmarked model shell, or a process in a coder turn) or "unplaced" (the gate could not place it).
func proposedBy() string {
	why := opgate.Refusal()
	switch {
	case why == "":
		return "operator"
	case opgate.Unplaced(why):
		return "unplaced"
	}
	if c := operator.Caller(); c != "" {
		return c
	}
	return "agent:unplaced"
}

// createProposal plans verb and argsJSON against the org and stores them as a pending proposal made by by: the one path
// of `org proposal create` and of the chat tools, so both judge, bound and store a proposal the same way.
func (env *orgEnv) createProposal(ctx context.Context, db *storage.Database, profileID, root string, doc *orgdesign.Doc, verb, argsJSON, conversation, by string) (map[string]interface{}, error) {
	plan, err := env.planProposal(ctx, db, profileID, doc, verb, argsJSON)
	if err != nil {
		return nil, err
	}
	p, err := orgproposal.NewStore(db.DB).Create(ctx, orgproposal.Input{ProfileID: profileID, Org: doc.Name, ConversationID: conversation,
		Verb: verb, Target: plan.Target, ArgsJSON: plan.ArgsJSON, PreviewText: plan.Preview, StateDigest: plan.Digest, CreatedBy: by})
	if err != nil {
		if known := env.proposalErr(err); known != err {
			return nil, known
		}
		return nil, fmt.Errorf("the proposal could not be stored; try again, or check the database") // not the database's own text
	}
	return env.proposalView(ctx, db, profileID, root, p), nil
}

func newOrgProposalCreateCmd(env *orgEnv) *cobra.Command {
	var verb, argsJSON, conversation string
	c := &cobra.Command{
		Use:   "create <org> --verb grant.add|grant.remove|autonomy.set --args-json '{...}'",
		Short: "Propose a grant or an autonomy change; nothing changes until the user confirms",
		Long: "Stores a proposal after checking its arguments the way the command it stands for would, and prints it with the " +
			"text the user is shown and the hash they confirm. grant.add takes role, automation and optionally mode, wait, " +
			"timeout_seconds, max_calls_per_run, max_calls_per_day, max_output_bytes (a grant a proposal makes always asks first). " +
			"grant.remove takes role and automation (the grant must exist). " +
			"autonomy.set takes level (manual or mid), decider, decider_runtime, decider_model, decider_threshold, on_decider_failure. " +
			"A newer proposal for the same role and automation (or the same org's autonomy) supersedes the older pending one; a conversation label holds at most 5 pending per org (conversation_id is a label, never ownership).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			db, profileID, root, doc, err := loadOrgForEdit(env, args[0])
			if err != nil {
				return err
			}
			m, err := env.createProposal(ctx, db, profileID, root, doc, verb, argsJSON, conversation, proposedBy())
			if err != nil {
				return err
			}
			return printJSONValue(map[string]interface{}{"v": 1, "proposal": m})
		},
	}
	c.Flags().StringVar(&verb, "verb", "", "grant.add | grant.remove | autonomy.set")
	c.Flags().StringVar(&argsJSON, "args-json", "", "The arguments, as one JSON object")
	c.Flags().StringVar(&conversation, "conversation", "", "The chat conversation the proposal belongs to")
	_ = c.MarkFlagRequired("verb")
	_ = c.MarkFlagRequired("args-json")
	return c
}

func newOrgProposalListCmd(env *orgEnv) *cobra.Command {
	var org, conversation string
	c := &cobra.Command{
		Use:   "list",
		Short: "List proposals (decided ones are kept 30 days)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			list, err := orgproposal.NewStore(db.DB).List(cmd.Context(), profileID, org, conversation)
			if err != nil {
				return err
			}
			out := []map[string]interface{}{}
			for i := range list {
				out = append(out, env.proposalView(cmd.Context(), db, profileID, root, &list[i]))
			}
			return printJSONValue(map[string]interface{}{"v": 1, "proposals": out})
		},
	}
	c.Flags().StringVar(&org, "org", "", "Only this org's proposals")
	c.Flags().StringVar(&conversation, "conversation", "", "Only this conversation's proposals")
	return c
}

func newOrgProposalShowCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "show <id>",
		Short: "Show a proposal with its hash recomputed from the stored row",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			p, err := orgproposal.NewStore(db.DB).Get(cmd.Context(), profileID, args[0])
			if err != nil {
				return env.proposalErr(err)
			}
			return printJSONValue(map[string]interface{}{"v": 1, "proposal": env.proposalView(cmd.Context(), db, profileID, root, p)})
		},
	}
}
