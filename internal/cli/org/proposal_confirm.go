package org

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/orgdesign"
	"github.com/monoes/mono-agent/internal/orgproposal"
)

// jsonCapture, when set, receives what printOrgJSON would print: confirm runs the sibling command in this process and
// prints one document of its own around the sibling's.
var jsonCapture atomic.Pointer[bytes.Buffer]

// applyMu keeps one apply at a time in a process, so a capture is never shared.
var applyMu sync.Mutex

// refuseProposalDecision is the gate of confirm and decline. It asks first, before the id is looked up, so a
// refused caller learns nothing about any row; nothing stored in a row stands in for the answer.
func refuseProposalDecision(what string) error {
	if why := opgate.Refusal(); why != "" {
		return clicore.OperatorOnly("%s is the user's to do: %s; run it yourself from a terminal or the app", what, why)
	}
	return nil
}

func newOrgProposalDeclineCmd(env *orgEnv) *cobra.Command {
	return &cobra.Command{
		Use:   "decline <id>",
		Short: "Decline a pending proposal (operator only)",
		Long:  "Ends a pending proposal without applying it. " + clicore.OperatorOnlyHelp,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := refuseProposalDecision("declining a proposal"); err != nil {
				return err
			}
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			st := orgproposal.NewStore(db.DB)
			if err := st.Decline(cmd.Context(), profileID, args[0]); err != nil {
				return env.proposalErr(err)
			}
			p, err := st.Get(cmd.Context(), profileID, args[0])
			if err != nil {
				return env.proposalErr(err)
			}
			return printJSONValue(map[string]interface{}{"v": 1, "proposal": env.proposalView(cmd.Context(), db, profileID, root, p)})
		},
	}
}

// The apply runs the sibling command, `org grant add`, `org grant remove` or `org autonomy set`, in THIS process, so the gate's verdict
// stays in the process that was asked (see the note on runSiblingInProcess). Its own gate and every check of its own
// run again; there is no other write path.
func newOrgProposalConfirmCmd(env *orgEnv) *cobra.Command {
	var hash string
	c := &cobra.Command{
		Use:   "confirm <id> --hash <hash>",
		Short: "Apply a pending proposal (operator only)",
		Long: "Applies a pending, unexpired proposal once, by running the real `org grant add` / `org grant remove` / `org autonomy set` with the " +
			"stored arguments. --hash is the hash `org proposal show` printed for what the user saw; it is refused if the stored " +
			"proposal no longer hashes to it, if the org changed since the proposal was made (stale), if it expired, or if it was " +
			"decided already. " + clicore.OperatorOnlyHelp,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			// First: before the id, the hash or the database is looked at.
			if err := refuseProposalDecision("confirming a proposal"); err != nil {
				return err
			}
			db, profileID, root, err := env.Profile()
			if err != nil {
				return err
			}
			st := orgproposal.NewStore(db.DB)
			p, err := st.Get(ctx, profileID, args[0])
			if err != nil {
				return env.proposalErr(err)
			}
			doc, _ := orgdesign.Load(root, p.Org)
			plan, planErr := env.planProposal(ctx, db, profileID, doc, p.Verb, p.ArgsJSON)
			if plan.ReadErr != nil { // not a verdict on the proposal: nothing is claimed, the row stays pending
				return plan.ReadErr
			}
			live := plan.Digest
			if live == p.StateDigest {
				// The org is as it was: the stored arguments must still pass what create checks, and the text the user
				// saw must still be what they render to.
				if planErr != nil {
					return planErr
				}
				if plan.Preview != p.PreviewText || plan.ArgsJSON != p.ArgsJSON {
					return env.proposalErr(orgproposal.ErrHashMismatch)
				}
			}
			claimed, err := st.Claim(ctx, profileID, p.ID, hash, live)
			if err != nil {
				return env.proposalErr(err)
			}
			result, applyErr := env.runSiblingInProcess(ctx, plan.sub(env), claimed.Org, plan.argv)
			// The row keeps a code and a fixed sentence, as a failed one does: the sibling's output (its warnings carry
			// error text and paths) goes to this command's own output only, and show is open to agents.
			status, stored := orgproposal.StatusApplied, `{"code":"applied","message":"applied by the user's confirm; the confirm output has the command's result"}`
			if applyErr != nil {
				status = orgproposal.StatusFailed
				stored = failureResult(applyErr) // show is open to agents: the raw error goes only to this command's own output
			}
			if err := st.Finish(ctx, profileID, claimed.ID, status, stored); err != nil && applyErr == nil {
				return err
			}
			if applyErr != nil {
				return applyErr
			}
			done, err := st.Get(ctx, profileID, claimed.ID)
			if err != nil {
				return err
			}
			return printJSONValue(map[string]interface{}{"v": 1, "proposal": env.proposalView(ctx, db, profileID, root, done), "result": json.RawMessage(result)})
		},
	}
	c.Flags().StringVar(&hash, "hash", "", "The hash of the proposal as shown by org proposal show")
	_ = c.MarkFlagRequired("hash")
	return c
}

// runSiblingInProcess runs the sibling command c, built from the same constructor the command tree uses, with argv as
// its flags and org as its argument, and returns the one JSON document it prints.
//
// In process, not a self-exec (runSelfExec of the chat tools): the question "is this the user's own terminal" is
// answered by opgate from this process's environment, its ancestry and the turn registry. A child process asks the
// same questions of its own, so the verdict would be taken twice by two processes that could differ (a child that
// starts with another environment, a binary found by path), and a test binary has no binary to exec. Running the
// command here keeps the verdict in the process that confirmed, and the command's own gate (grant add's and
// autonomy set's, which also decides the re-sign and the class-B limits) runs again in the same process.
func (env *orgEnv) runSiblingInProcess(ctx context.Context, c *cobra.Command, org string, argv []string) (json.RawMessage, error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	buf := &bytes.Buffer{}
	jsonCapture.Store(buf)
	defer jsonCapture.Store(nil)
	c.SetContext(ctx)
	if err := c.ParseFlags(argv); err != nil {
		return nil, err
	}
	if err := c.RunE(c, []string{org}); err != nil {
		return nil, err
	}
	out := strings.TrimSpace(buf.String())
	if !json.Valid([]byte(out)) {
		return json.RawMessage(`null`), nil
	}
	return json.RawMessage(out), nil
}

// failureResult is what the row keeps of a failed apply: a class and a fixed sentence, no text of the error (paths,
// secrets and database text can be in it).
func failureResult(err error) string {
	code, msg := "failed", "the apply failed; the operator's confirm output has the reason"
	if strings.Contains(err.Error(), "is the user's to do") {
		code, msg = "refused", "the command refused: it is the user's to do from their own terminal or the app"
	}
	b, _ := json.Marshal(map[string]string{"code": code, "message": msg})
	return string(b)
}
