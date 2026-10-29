package task

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The person's side of the upward ask channel (coderhost/ask_ancestor.go): a critical ask reaches no model, it is
// a person item of kind ancestor_question that only the person answers. `inbox` shows it with the question; this
// verb answers it. The question is text another agent wrote: it is shown as data, never run.
//
//	answer-ask --ask ID --text T   the answer reaches the asking lead as sealed quoted data; the item closes

// fillAskView adds what an ancestor_question item shows: the ask it points to. An ask that cannot be read leaves the
// bare item (the person is still told something waits).
func fillAskView(v *personItemView, cs *coderhost.Store, profileID string, it coderhost.PersonItem) {
	a, err := cs.GetAsk(profileID, it.RefID)
	if err != nil {
		return
	}
	v.AskID, v.Question, v.Importance, v.Level = a.ID, a.Question, a.Importance, a.Level
	v.AskerTitle, v.AncestorTitle, v.Conversation = a.AskerTitle, a.AncestorTitle, a.AskerConv
}

func (d Deps) newPersonAnswerAskCmd() *cobra.Command {
	var askID int64
	var text string
	cmd := &cobra.Command{
		Use:   "answer-ask --ask ID --text TEXT",
		Short: "Answer a critical ask that a lead raised for you; the answer reaches the asking lead as quoted data and the item closes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := d.callerFor(flagAs(cmd)).operator(personWhat); err != nil {
				return err
			}
			if askID <= 0 {
				return d.InvalidInput("task session person answer-ask needs the ask id (--ask)")
			}
			if text == "" {
				return d.InvalidInput("task session person answer-ask needs your answer (--text)")
			}
			return d.withTasks(cmd, func(_ context.Context, _ *tasks.Store, p tasks.Profile) error {
				db, err := d.OpenDB()
				if err != nil {
					return err
				}
				defer db.Close()
				a, c, err := coderhost.NewStore(db.DB).AnswerAncestorQuestion(p.ID, askID, text)
				switch {
				case errors.Is(err, coderhost.ErrNoQuestion), errors.Is(err, coderhost.ErrNotOpen):
					return taskCLIError{error: d.NotFound("%v", err), code: "not_found"}
				case errors.Is(err, coderhost.ErrInvalid):
					return d.InvalidInput("%v", err)
				case err != nil:
					return err
				}
				if d.JSONOutput() {
					return d.WriteJSON(cmd.OutOrStdout(), map[string]any{"profile": p, "askId": a.ID, "status": a.Status, "commandId": c.ID})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Answered ask #%d; the asking lead gets it as quoted data.\n", a.ID)
				return nil
			})
		},
	}
	cmd.Flags().Int64Var(&askID, "ask", 0, "The ask id, from the inbox (required)")
	cmd.Flags().StringVar(&text, "text", "", "Your answer (required), sent to the asking lead as quoted data")
	return cmd
}
