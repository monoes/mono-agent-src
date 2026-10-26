package chat

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// `coder sessions decisions|questions`: the ledger of decisions made for a session
// without asking a person, and the questions parked while nobody could answer.
// Reading is open; reverting a decision and answering a question are the
// operator's.

func parseID(s, what string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, clicore.InvalidInput("%q is not a %s id", s, what)
	}
	return id, nil
}

func newCoderSessionsDecisionsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "decisions",
		Short: "The decisions made for a session without asking a person: list, show, revert",
	}
	cmd.AddCommand(newDecisionsListCmd(cfg), newDecisionsShowCmd(cfg), newDecisionsRevertCmd(cfg))
	return cmd
}

func withCoderStore(cfg *Deps, conv string, f func(cs *coderhost.Store, profileID string) error) error {
	profileID, err := coderConversation(cfg, conv)
	if err != nil {
		return err
	}
	db, err := cfg.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	return f(coderhost.NewStore(db.DB), profileID)
}

func newDecisionsListCmd(cfg *Deps) *cobra.Command {
	var class, status string
	var limit int
	cmd := &cobra.Command{
		Use:   "list CONVERSATION",
		Short: "List a session's decisions, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withCoderStore(cfg, args[0], func(cs *coderhost.Store, profileID string) error {
				rows, err := cs.ListDecisions(profileID, args[0], coderhost.LedgerFilter{Class: class, Status: status, Limit: limit})
				if err != nil {
					return err
				}
				return printQueueJSON(cmd, map[string]any{"conversationId": args[0], "decisions": rows})
			})
		},
	}
	cmd.Flags().StringVar(&class, "class", "", "Only this class of gap")
	cmd.Flags().StringVar(&status, "status", "", "Only active or reverted decisions")
	cmd.Flags().IntVar(&limit, "limit", 0, "At most this many (default 100, most 500)")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newDecisionsShowCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show CONVERSATION ID",
		Short: "Show one decision",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseID(args[1], "decision")
			if err != nil {
				return err
			}
			return withCoderStore(cfg, args[0], func(cs *coderhost.Store, profileID string) error {
				e, ok, err := cs.GetDecision(profileID, args[0], id)
				if err != nil {
					return err
				}
				if !ok {
					return clicore.NotFound("no decision %d for %s", id, args[0])
				}
				return printQueueJSON(cmd, e)
			})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newDecisionsRevertCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "revert CONVERSATION ID",
		Short: "Mark a reversible decision reverted (the operator only)",
		Long: "Marks the decision reverted in the ledger and records who did it. It does not start a turn and does not " +
			"tell the session: to have it undo the change, send it a message with `coder sessions send`.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, "revert a coder session's decision"); err != nil {
				return err
			}
			id, err := parseID(args[1], "decision")
			if err != nil {
				return err
			}
			return withCoderStore(cfg, args[0], func(cs *coderhost.Store, profileID string) error {
				e, err := cs.RevertDecision(profileID, args[0], id, "operator")
				switch {
				case errors.Is(err, coderhost.ErrNoDecision):
					return clicore.NotFound("no decision %d for %s", id, args[0])
				case errors.Is(err, coderhost.ErrNotReversible), errors.Is(err, coderhost.ErrAlreadyReverted):
					return clicore.InvalidInput("%v", err)
				case err != nil:
					return err
				}
				return printQueueJSON(cmd, e)
			})
		},
	}
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newCoderSessionsQuestionsCmd(cfg *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "questions",
		Short: "Questions parked while nobody could answer: list, answer",
	}
	cmd.AddCommand(newQuestionsListCmd(cfg), newQuestionsAnswerCmd(cfg))
	return cmd
}

func newQuestionsListCmd(cfg *Deps) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list [CONVERSATION]",
		Short: "List parked questions (every conversation of the profile without an argument), with who may answer each",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conv := ""
			if len(args) == 1 {
				conv = args[0]
			}
			profileID := cfg.ProfileID()
			if conv != "" {
				var err error
				if profileID, err = coderConversation(cfg, conv); err != nil {
					return err
				}
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return err
			}
			defer db.Close()
			rows, err := coderhost.NewStore(db.DB).ListQuestions(profileID, conv, !all)
			if err != nil {
				return err
			}
			return printQueueJSON(cmd, map[string]any{"questions": rows})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Include the answered ones")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}

func newQuestionsAnswerCmd(cfg *Deps) *cobra.Command {
	var noHost bool
	cmd := &cobra.Command{
		Use:   "answer CONVERSATION ID ANSWER",
		Short: "Answer a parked question when the policy names the user (the operator only)",
		Long: "The answer is queued to the session as a message the host sends (a turn id starting with answer-). It " +
			"carries no authority: it cannot raise access or a budget, change a setting or confirm a mission.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := clicore.RequireOperator(cfg.RequireOperator, "answer a coder session's parked question"); err != nil {
				return err
			}
			id, err := parseID(args[1], "question")
			if err != nil {
				return err
			}
			var out coderhost.Question
			var cmdID int64
			err = withCoderStore(cfg, args[0], func(cs *coderhost.Store, profileID string) error {
				if q, qerr := cs.GetQuestion(profileID, id); qerr == nil && q.ConversationID != args[0] {
					return clicore.NotFound("no question %d for %s", id, args[0])
				}
				q, c, aerr := cs.Answer(profileID, id, args[2], coderhost.OperatorScope{})
				switch {
				case errors.Is(aerr, coderhost.ErrNoQuestion):
					return clicore.NotFound("no question %d for %s", id, args[0])
				case errors.Is(aerr, coderhost.ErrNotAnswerer), errors.Is(aerr, coderhost.ErrNotOpen), errors.Is(aerr, coderhost.ErrQueueFull):
					return clicore.InvalidInput("%v", aerr)
				case aerr != nil:
					return clicore.InvalidInput("%v", aerr)
				}
				out, cmdID = q, c.ID
				return nil
			})
			if err != nil {
				return err
			}
			if !noHost {
				if err := ensureHost(cfg); err != nil {
					return fmt.Errorf("the answer is queued as command %d and is delivered when a coder host runs; the host could not be started: %w", cmdID, err)
				}
			}
			return printQueueJSON(cmd, map[string]any{"question": out, "commandId": cmdID, "hostStarted": !noHost})
		},
	}
	cmd.Flags().BoolVar(&noHost, "no-host", false, "Queue only; do not start the host")
	clicore.WithJSONErrors(cfg.JSONOutput, cmd)
	return cmd
}
