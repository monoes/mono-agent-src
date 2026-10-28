package task

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// `task session person`: the person's side of the items a coder session raises for them (the
// person items of the coder host). A giant session's plan waits for the person with no timeout;
// the person lists what waits and answers it here. Every verb is the operator's: an agent can
// never answer a person item, whatever it holds.
//
//	approve ID        the operator's `session approve`, so the approval is recorded as the person's
//	deny ID --note T  the session stays a draft; the note is kept in its history and told to its lead
//	ask ID --text T   the question is told to the lead; the item stays open

const personWhat = "answer a person item"

// personItemView is one open item as the person sees it. A plan_approval item carries the session
// and why it is giant; any other kind carries what the item row itself holds.
type personItemView struct {
	ID               int64    `json:"id"`
	Kind             string   `json:"kind"`
	CreatedAt        string   `json:"createdAt"`
	LeadConversation string   `json:"leadConversationId"`
	SessionID        int64    `json:"sessionId,omitempty"`
	Conversation     string   `json:"conversationId,omitempty"` // the chat of the session itself: the agent's pane
	Title            string   `json:"title,omitempty"`
	Goal             string   `json:"goal,omitempty"`
	State            string   `json:"state,omitempty"`
	Reasons          []string `json:"reasons,omitempty"`
	TaskCount        int      `json:"taskCount,omitempty"`
	MaxTasks         int      `json:"maxTasks,omitempty"`
	BudgetUSD        float64  `json:"budgetUsd,omitempty"`
	BudgetShare      float64  `json:"budgetShare,omitempty"`
	Access           string   `json:"access,omitempty"`
	Depth            int      `json:"depth,omitempty"`
	RefID            int64    `json:"refId,omitempty"`
}

func (d Deps) newSessionPersonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "person",
		Short: "The items that wait for you, the person: list them, approve a giant plan, deny it with a note, or ask its lead more (you only)",
		Long: "A session that is giant work (more than 25 tasks, a large share of the mission's budget, more access than its parent, " +
			"a level beyond the working depth) is never approved by an agent: it raises a person item that waits with no timeout. " +
			"`list` shows what waits and why; `approve` approves the plan (recorded as the person's approval); `deny --note` leaves the " +
			"session a draft and sends the note to its lead; `ask --text` sends a question to the lead and leaves the item open.",
	}
	cmd.AddCommand(d.newPersonListCmd(), d.newPersonApproveCmd(), d.newPersonDenyCmd(), d.newPersonAskCmd())
	for _, sub := range cmd.Commands() {
		d.WithJSONErrors(sub)
	}
	return cmd
}

func (d Deps) newPersonListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the items that wait for you, oldest first, with why a plan is giant work",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator("list the person items")
			if err != nil {
				return err
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				db, err := d.OpenDB()
				if err != nil {
					return err
				}
				defer db.Close()
				items, err := coderhost.NewStore(db.DB).OpenPersonItems(p.ID)
				if err != nil {
					return err
				}
				views := make([]personItemView, 0, len(items))
				for _, it := range items {
					views = append(views, d.personView(ctx, store, p, it, actor))
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "items": views})
				}
				w := cmd.OutOrStdout()
				if len(views) == 0 {
					fmt.Fprintln(w, "Nothing waits for you.")
				}
				for _, v := range views {
					if v.SessionID == 0 {
						fmt.Fprintf(w, "%s  item %d  (answer it where it was raised)\n", v.Kind, v.ID)
						continue
					}
					fmt.Fprintf(w, "%s  session #%d  %s  %d tasks, %s\n", v.Kind, v.SessionID, taskCut(v.Title, 60), v.TaskCount, money(v.BudgetUSD))
					for _, r := range v.Reasons {
						fmt.Fprintf(w, "    giant: %s\n", taskCut(r, 160))
					}
				}
				return nil
			})
		},
	}
}

// personView fills what the item row does not hold. A session that cannot be read is listed
// as the bare item: the person is still told something waits.
func (d Deps) personView(ctx context.Context, store *tasks.Store, p tasks.Profile, it coderhost.PersonItem, actor tasks.Actor) personItemView {
	v := personItemView{ID: it.ID, Kind: it.Kind, CreatedAt: it.CreatedAt, LeadConversation: it.ConversationID}
	if it.Kind != coderhost.ItemPlanApproval {
		v.RefID = it.RefID
		return v
	}
	v.SessionID = it.RefID
	se, err := store.GetSession(ctx, p.ID, it.RefID, actor)
	if err != nil {
		return v
	}
	v.Conversation, v.Title, v.Goal, v.State = se.ConversationID, se.Title, se.Goal, string(se.State)
	v.MaxTasks, v.Access, v.Depth = se.MaxTasks, se.AccessLevel, se.Depth
	if f, err := store.GiantFactsOf(ctx, p.ID, it.RefID, actor); err == nil {
		v.Reasons, v.TaskCount, v.BudgetUSD, v.BudgetShare = f.Reasons, f.TaskCount, f.BudgetUSD, f.BudgetShare
	}
	return v
}

// personAnswer runs one answering verb: the operator check comes first, then the session id, then
// the open item of that session. A session with no open item is refused, so these verbs cannot be
// used to approve or answer for a plan nobody raised.
func (d Deps) personAnswer(use, short string, wordsFlag, wordsHelp string, run func(cmd *cobra.Command, store *tasks.Store, cs *coderhost.Store, p tasks.Profile, it coderhost.PersonItem, actor tasks.Actor, text string) error) *cobra.Command {
	var words string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := d.callerFor(flagAs(cmd)).operator(personWhat)
			if err != nil {
				return err
			}
			if len(args) != 1 {
				return d.InvalidInput("task session person %s takes one session id, written 3 or #3 (got %d arguments)", strings.Fields(use)[0], len(args))
			}
			id, err := d.parseSessionID(args[0])
			if err != nil {
				return err
			}
			if wordsFlag != "" && strings.TrimSpace(words) == "" {
				return d.InvalidInput("task session person %s needs the words to send (--%s)", strings.Fields(use)[0], wordsFlag)
			}
			return d.withTasks(cmd, func(ctx context.Context, store *tasks.Store, p tasks.Profile) error {
				db, err := d.OpenDB()
				if err != nil {
					return err
				}
				defer db.Close()
				cs := coderhost.NewStore(db.DB)
				it, ok, err := cs.PlanApprovalItem(p.ID, id)
				if err != nil {
					return err
				}
				if !ok {
					return d.NotFound("no open person item asks for the approval of session #%d", id)
				}
				return run(cmd, store, cs, p, it, actor, strings.TrimSpace(words))
			})
		},
	}
	if wordsFlag != "" {
		cmd.Flags().StringVar(&words, wordsFlag, "", wordsHelp)
	}
	return cmd
}

func (d Deps) newPersonApproveCmd() *cobra.Command {
	return d.personAnswer("approve ID", "Approve the plan of a giant session: its tasks become Ready and it runs (recorded as the person's approval)", "", "",
		func(cmd *cobra.Command, store *tasks.Store, _ *coderhost.Store, p tasks.Profile, it coderhost.PersonItem, actor tasks.Actor, _ string) error {
			se, err := store.ApproveSession(cmd.Context(), p.ID, it.RefID, actor)
			if err != nil {
				return d.taskErr(err)
			}
			return d.writeSession(cmd, p, se, "Approved")
		})
}

func (d Deps) newPersonDenyCmd() *cobra.Command {
	return d.personAnswer("deny ID --note TEXT", "Turn the plan down: the session stays a draft, the note is kept in its history and sent to its lead",
		"note", "Why (required): kept in the session's history and sent to its lead",
		func(cmd *cobra.Command, store *tasks.Store, cs *coderhost.Store, p tasks.Profile, it coderhost.PersonItem, actor tasks.Actor, note string) error {
			if _, err := cs.TellLeadAboutPlan(cmd.Context(), p.ID, it.ConversationID, it.RefID, coderhost.PlanDenied, note); err != nil {
				return d.InvalidInput("%v", err)
			}
			if err := store.AddSessionEvent(cmd.Context(), p.ID, it.RefID, tasks.EventNote, "the person turned the plan down: "+note, actor); err != nil {
				return d.taskErr(err)
			}
			if err := cs.ClosePlanApprovalItem(p.ID, it.RefID); err != nil {
				return err
			}
			se, err := store.GetSession(cmd.Context(), p.ID, it.RefID, actor)
			if err != nil {
				return d.taskErr(err)
			}
			return d.writeSession(cmd, p, se, "Denied")
		})
}

func (d Deps) newPersonAskCmd() *cobra.Command {
	return d.personAnswer("ask ID --text TEXT", "Send a question about the plan to its lead; the item stays open",
		"text", "Your question (required), sent to the lead as quoted data",
		func(cmd *cobra.Command, _ *tasks.Store, cs *coderhost.Store, p tasks.Profile, it coderhost.PersonItem, actor tasks.Actor, text string) error {
			c, err := cs.TellLeadAboutPlan(cmd.Context(), p.ID, it.ConversationID, it.RefID, coderhost.PlanAsked, text)
			if err != nil {
				return d.InvalidInput("%v", err)
			}
			if d.JSONOutput() {
				return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "sessionId": it.RefID, "commandId": c.ID, "item": "open"})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Asked the lead of session #%d; the item stays open.\n", it.RefID)
			return nil
		})
}
