package people

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/peoplereview"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/spf13/cobra"
)

// newPeopleReviewCmd is the review queue for people a workflow staged for
// outreach (category "pending_approval" with a drafted introduction). The
// GUI's Human in Loop page is a front end for these commands.
func (d Deps) newPeopleReviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review people staged for outreach: list, approve (and send), reject",
		Long: "Workflows can stage a lead for human review by saving it with category " +
			"\"pending_approval\" and a drafted introduction. `review list` shows the queue; " +
			"`review approve` marks a person approved, optionally editing the introduction and " +
			"sending it through a dispatch workflow; `review reject` drops them from the queue.",
	}
	cmd.AddCommand(d.newPeopleReviewListCmd(), d.newPeopleReviewApproveCmd(), d.newPeopleReviewRejectCmd())
	return cmd
}

func (d Deps) newPeopleReviewListCmd() *cobra.Command {
	var suggest, resuggest bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List people awaiting review",
		Long: "List people awaiting review.\n\n" +
			"--suggest adds TypeSafe Jev's suggestion to each person: {suggest approve|reject, p, " +
			"intro_fit on_topic|generic|off, intro_fit_p}. It needs `jev enable people_review`; a " +
			"suggestion is computed once per person (one request each) and cached until the person " +
			"or their introduction changes. --resuggest recomputes. Suggestions never approve or " +
			"reject anyone.",
		Example: `  monoagentcli --json people review list
  monoagentcli --json people review list --suggest`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			people, err := peoplereview.ListPending(cmd.Context(), db.DB, d.ProfileID())
			if err != nil {
				return err
			}
			if !suggest && !resuggest {
				if d.JSONOutput() {
					return printReviewJSON(people)
				}
				if len(people) == 0 {
					fmt.Println("No one is waiting for review.")
					return nil
				}
				table := d.NewTable(os.Stdout, []string{"ID", "Platform", "Username", "Name", "Introduction"}, nil)
				for _, p := range people {
					table.Append([]string{p.ID, p.Platform, truncateStr(p.PlatformUsername, 20),
						truncateStr(p.FullName, 20), truncateStr(p.Introduction, 40)})
				}
				table.Render()
				return nil
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, cancel := context.WithTimeout(ctx, reviewSuggestTimeout)
			defer cancel()
			reviewed, warns := peoplereview.WithSuggestions(ctx, db.DB, d.ProfileID(), people, resuggest)
			for _, w := range warns {
				fmt.Fprintln(os.Stderr, "warning: "+w)
			}
			if d.JSONOutput() {
				return printReviewJSON(reviewed)
			}
			if len(reviewed) == 0 {
				fmt.Println("No one is waiting for review.")
				return nil
			}
			table := d.NewTable(os.Stdout, []string{"ID", "Platform", "Username", "Name", "Introduction", "Suggestion"}, nil)
			for _, r := range reviewed {
				sug := ""
				if s := r.Suggestion; s != nil {
					sug = fmt.Sprintf("%s p=%.2f intro=%s", s.Suggest, s.P, s.IntroFit)
				}
				table.Append([]string{r.ID, r.Platform, truncateStr(r.PlatformUsername, 20),
					truncateStr(r.FullName, 20), truncateStr(r.Introduction, 40), sug})
			}
			table.Render()
			return nil
		},
	}
	cmd.Flags().BoolVar(&suggest, "suggest", false, "Add TypeSafe Jev's suggestion per person (cached; needs 'jev enable people_review')")
	cmd.Flags().BoolVar(&resuggest, "resuggest", false, "Recompute every suggestion (implies --suggest)")
	return cmd
}

// reviewSuggestTimeout bounds one `people review list --suggest` run's Jev calls.
const reviewSuggestTimeout = 60 * time.Second

// reviewSend is the dispatch run for an approved person: which workflow,
// and the trigger input it gets.
type reviewSend struct {
	WorkflowID   string                 `json:"workflow_id"`
	WorkflowName string                 `json:"workflow_name"`
	Input        map[string]interface{} `json:"input"`
}

type reviewApproval struct {
	Person peoplereview.Person `json:"person"`
	Send   *reviewSend         `json:"send,omitempty"`
}

func (d Deps) newPeopleReviewApproveCmd() *cobra.Command {
	var (
		intro        string
		send         bool
		sendWorkflow string
		sendPlan     bool
	)
	cmd := &cobra.Command{
		Use:   "approve <person-id>",
		Short: "Approve a person, optionally sending their introduction",
		Long: "Marks a person approved. --intro replaces the drafted introduction.\n\n" +
			"--send then runs the dispatch workflow with the person as trigger input " +
			"({person_id, platform, platform_username, introduction}) and waits for it. The " +
			"workflow is --send-workflow (an id or part of a name) or, by convention, the " +
			"profile's one workflow named like \"" + peoplereview.SendWorkflowName + "\". It is " +
			"resolved before approving, so a missing, ambiguous or inactive workflow leaves the " +
			"person in the queue.\n\n" +
			"--send-plan approves and prints the dispatch run (workflow and input) without " +
			"starting it, for a caller that runs workflows itself (the GUI).",
		Example: `  monoagentcli people review approve 3f2a…
  monoagentcli people review approve 3f2a… --intro "Hi Sam, …" --send
  monoagentcli people review approve 3f2a… --send --send-workflow "LinkedIn DMs"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if send && sendPlan {
				return d.InvalidInput("--send and --send-plan are exclusive")
			}
			if sendWorkflow != "" && !send && !sendPlan {
				return d.InvalidInput("--send-workflow needs --send or --send-plan")
			}
			ctx := cmd.Context()
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()

			// Everything that can refuse the send is checked before the
			// person leaves the queue.
			var wf peoplereview.Workflow
			if send || sendPlan {
				if wf, err = d.pickReviewWorkflow(ctx, db, sendWorkflow); err != nil {
					return err
				}
			}
			p, err := peoplereview.Approve(ctx, db.DB, d.ProfileID(), args[0], intro)
			if err != nil {
				return d.reviewErr(err)
			}
			res := reviewApproval{Person: p}
			if send || sendPlan {
				res.Send = &reviewSend{WorkflowID: wf.ID, WorkflowName: wf.Name, Input: peoplereview.SendInput(p)}
			}
			if !send {
				if d.JSONOutput() {
					return printReviewJSON(res)
				}
				fmt.Printf("Approved %s (%s).\n", displayName(p), p.ID)
				if res.Send != nil {
					fmt.Printf("Send with workflow %q (%s).\n", wf.Name, wf.ID)
				}
				return nil
			}

			if !d.JSONOutput() {
				fmt.Printf("Approved %s (%s); sending with workflow %q.\n", displayName(p), p.ID, wf.Name)
			}
			input, err := json.Marshal(res.Send.Input)
			if err != nil {
				return err
			}
			db.Close() // the run opens its own
			if err := d.RunWorkflow(ctx, []string{wf.ID, "--input", string(input)}); err != nil {
				return fmt.Errorf("%s is approved, but sending failed: %w", p.ID, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&intro, "intro", "", "Replace the drafted introduction")
	cmd.Flags().BoolVar(&send, "send", false, "Run the dispatch workflow for this person after approving")
	cmd.Flags().StringVar(&sendWorkflow, "send-workflow", "", `Dispatch workflow id or name (default: the one named like "`+peoplereview.SendWorkflowName+`")`)
	cmd.Flags().BoolVar(&sendPlan, "send-plan", false, "Approve and print the dispatch run without starting it")
	return cmd
}

func (d Deps) newPeopleReviewRejectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reject <person-id>",
		Short: "Reject a person so they leave the queue",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := d.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			if err := peoplereview.Reject(cmd.Context(), db.DB, d.ProfileID(), args[0]); err != nil {
				return d.reviewErr(err)
			}
			if d.JSONOutput() {
				return printReviewJSON(map[string]string{"id": args[0], "category": peoplereview.Rejected})
			}
			fmt.Printf("Rejected %s.\n", args[0])
			return nil
		},
	}
}

// pickReviewWorkflow resolves the dispatch workflow among the profile's
// workflows (files and SQLite alike) and refuses an inactive one, which
// the engine would not run.
func (d Deps) pickReviewWorkflow(ctx context.Context, db *storage.Database, want string) (peoplereview.Workflow, error) {
	all, err := d.WorkflowStore(db).ListWorkflows(ctx, d.ProfileID())
	if err != nil {
		return peoplereview.Workflow{}, fmt.Errorf("listing workflows: %w", err)
	}
	cands := make([]peoplereview.Workflow, len(all))
	for i, w := range all {
		cands[i] = peoplereview.Workflow{ID: w.ID, Name: w.Name, Active: w.IsActive}
	}
	wf, err := peoplereview.PickSendWorkflow(cands, want)
	if err != nil {
		return wf, d.reviewErr(err)
	}
	if !wf.Active {
		return wf, d.InvalidInput("workflow %q (%s) is inactive — activate it first: monoagentcli workflow activate %s", wf.Name, wf.ID, wf.ID)
	}
	return wf, nil
}

func (d Deps) reviewErr(err error) error {
	switch {
	case errors.Is(err, peoplereview.ErrNotFound):
		return d.NotFound("%v", err)
	case errors.Is(err, peoplereview.ErrAmbiguous):
		return d.InvalidInput("%v", err)
	}
	return err
}

func displayName(p peoplereview.Person) string {
	if p.FullName != "" {
		return p.FullName
	}
	return p.PlatformUsername
}

func printReviewJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
