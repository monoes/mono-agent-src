package chat

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
)

func newChatHistoryTurnsCmd(cfg *Deps) *cobra.Command {
	var cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "turns <conversation-id>",
		Short: "List a conversation's turns, newest first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := chatLimit(limit, 50, 200)
			if err != nil {
				return err
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if _, err := store.GetConversation(args[0], profileID); err != nil {
				return chatStoreErr(err)
			}
			turns, next, err := store.ListTurns(args[0], profileID, cursor, n)
			if err != nil {
				return err
			}
			page := chatTurnPage{Items: []ai.TurnRecord{}, NextCursor: next}
			for _, t := range turns {
				page.Items = append(page.Items, t.Record())
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(page)
			}
			for _, t := range page.Items {
				fmt.Printf("%s  %-11s %s  %s\n", t.ID, t.Status, t.CreatedAt, firstLineOf(t.Prompt))
			}
			if next != "" {
				fmt.Printf("more: --cursor %q\n", next)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cursor, "cursor", "", "next_cursor from the previous page")
	cmd.Flags().IntVar(&limit, "limit", 0, "Page size (default 50, max 200)")
	return cmd
}

func newChatHistoryTurnCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "turn <conversation-id> <turn-id>",
		Short: "Show one turn",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			t, err := turnInConversation(store, profileID, args[0], args[1])
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(t.Record())
			}
			fmt.Printf("id:       %s\nstatus:   %s\nreason:   %s\nlast seq: %d\ncreated:  %s\nprompt:   %s\n",
				t.ID, t.Status, t.Reason, t.LastCommittedSeq, t.CreatedAt, t.Prompt)
			return nil
		},
	}
}

func newChatHistoryEventsCmd(cfg *Deps) *cobra.Command {
	var afterSeq int64
	var limit int
	var agent string
	cmd := &cobra.Command{
		Use:   "events <conversation-id> <turn-id>",
		Short: "Show a turn's events after --after-seq, oldest first, with the turn's status",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := chatLimit(limit, 500, 1000)
			if err != nil {
				return err
			}
			if afterSeq < 0 {
				return clicore.InvalidInput("--after-seq must not be negative")
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			t, err := turnInConversation(store, profileID, args[0], args[1])
			if err != nil {
				return err
			}
			evs, err := store.GetEvents(args[0], args[1], profileID, afterSeq, n)
			if err != nil {
				return err
			}
			page := chatEventPage{Items: []chatevents.Record{}, Turn: t.Record(), LastCommittedSeq: t.LastCommittedSeq, HasMore: len(evs) == n}
			for _, ev := range evs {
				// --agent keeps one dynamic-org agent's events; has_more
				// and paging still follow the unfiltered page.
				if r := ev.Record(); agent == "" || eventAgentID(r) == agent {
					page.Items = append(page.Items, r)
				}
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(page)
			}
			for _, ev := range page.Items {
				fmt.Printf("%4d  %s  %-15s %s\n", ev.Seq, ev.At, ev.Type, ev.Payload)
			}
			fmt.Printf("turn %s: %s (last seq %d)\n", t.ID, t.Status, t.LastCommittedSeq)
			return nil
		},
	}
	cmd.Flags().Int64Var(&afterSeq, "after-seq", 0, "Only events with a higher seq")
	cmd.Flags().IntVar(&limit, "limit", 0, "Maximum events (default 500, max 1000); has_more is true when a full page came back")
	cmd.Flags().StringVar(&agent, "agent", "", "Dynamic org: only this agent's events (a worker id such as w1, or lead)")
	return cmd
}

// parseTurnStatus accepts only the terminal statuses.
func parseTurnStatus(s string) (chatevents.TurnStatus, error) {
	switch st := chatevents.TurnStatus(s); st {
	case chatevents.StatusCompleted, chatevents.StatusFailed, chatevents.StatusCancelled, chatevents.StatusInterrupted:
		return st, nil
	}
	return "", clicore.InvalidInput("--status must be completed, failed, cancelled or interrupted (got %q)", s)
}

func newChatHistoryFinishCmd(cfg *Deps) *cobra.Command {
	var status, reason, exitCode string
	cmd := &cobra.Command{
		Use:   "finish <conversation-id> <turn-id>",
		Short: "Finalize an active turn whose runner died, recording its turn.finished event",
		Long: "Moves an active turn to a terminal status and journals its turn.finished event, once: " +
			"a turn that is already finished is left alone (finalized: false). The desktop app calls " +
			"this when a `chat --turn` process it stopped or lost exits without finishing its own turn.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := parseTurnStatus(status)
			if err != nil {
				return err
			}
			var code *int
			if exitCode != "" {
				v, err := strconv.Atoi(exitCode)
				if err != nil {
					return clicore.InvalidInput("--exit-code must be an integer")
				}
				code = &v
			}
			store, profileID, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			if _, err := turnInConversation(store, profileID, args[0], args[1]); err != nil {
				return err
			}
			ev, already, err := store.FinalizeTurn(profileID, args[0], args[1], st, reason, code, true)
			if err != nil {
				return err
			}
			t, err := store.GetTurn(args[1], profileID)
			if err != nil {
				return chatStoreErr(err)
			}
			res := chatFinishResult{Finalized: !already, Turn: t.Record()}
			if !already {
				rec := ev.Record()
				res.Event = &rec
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(res)
			}
			if already {
				fmt.Printf("turn %s was already %s\n", t.ID, t.Status)
			} else {
				fmt.Printf("turn %s: %s\n", t.ID, t.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "Terminal status: completed, failed, cancelled or interrupted")
	cmd.Flags().StringVar(&reason, "reason", "", "Human-readable reason")
	cmd.Flags().StringVar(&exitCode, "exit-code", "", "Process exit code to record")
	return cmd
}

func newChatHistoryReconcileCmd(cfg *Deps) *cobra.Command {
	var exceptOwner string
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Mark every turn left active, in every profile, as interrupted (acts on every profile)",
		Long: "Acts on every profile, not one: marks every turn still active as interrupted, and removes the " +
			"`chat turn stop` mailbox folders of turn processes that are gone. The desktop app runs " +
			"this at startup with --except-owner set to its own instance id, so turns it has already " +
			"started are skipped. There is no liveness check: a second running app's turns are " +
			"interrupted too.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, closeDB, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			skip, closeSkip := hostHeldTurns(cfg)
			turns, errs := store.ReconcileActiveTurnsSkipping(exceptOwner, skip)
			closeSkip()
			// Mailbox folders of turns whose process died (#255).
			sweepAgentControl(agentControlRoot(cfg), time.Now())
			res := chatReconcileResult{Reconciled: []ai.TurnRecord{}, Errors: []string{}, Worktrees: reconcileWorktrees(cmd.Context(), store)}
			for _, t := range turns {
				res.Reconciled = append(res.Reconciled, t.Record())
			}
			for _, e := range errs {
				res.Errors = append(res.Errors, e.Error())
			}
			if cfg.JSONOutput() {
				if err := clicore.PrintJSON(res); err != nil {
					return err
				}
			} else {
				fmt.Printf("interrupted %d turn(s)\n", len(res.Reconciled))
				for _, w := range res.Worktrees {
					switch {
					case w.Error != "":
						fmt.Printf("kept worktree %s: %s\n", w.Path, w.Error)
					case w.BranchKept:
						fmt.Printf("removed worktree %s; kept unmerged branch %s\n", w.Path, w.Branch)
					}
				}
			}
			if len(errs) > 0 {
				return fmt.Errorf("reconcile: %d turn(s) failed: %s", len(errs), errs[0])
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&exceptOwner, "except-owner", "", "Skip turns owned by this app instance id")
	return cmd
}

// reconcileWorktrees cleans up, in every coder folder, the worktrees of
// isolated org writers whose turn is no longer active (#230).
func reconcileWorktrees(ctx context.Context, store *ai.AIStore) []dynorg.TreeCleanup {
	out := []dynorg.TreeCleanup{}
	active, err := activeTurnIDs(store)
	if err != nil {
		return out
	}
	folders, err := store.CoderFolders()
	if err != nil {
		return out
	}
	for _, cwd := range folders {
		out = append(out, dynorg.ReconcileWorktrees(ctx, cwd, func(id string) bool { return active[id] })...)
	}
	return out
}
