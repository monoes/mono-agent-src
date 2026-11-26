package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitask/budget"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// The three limits a coder chat's message can run under.
const (
	spendCapMessage = "message" // the operator's coder budget, per message
	spendCapSession = "session" // what the coder session's budget has left
	spendCapTask    = "task"    // what the AI task's cap has left
)

// chatSpendCap is the spend cap of a conversation's next message: the lowest
// of the limits its turn really runs under (turnBudget, coderhost's TurnCap
// and the task ledger). The AI-task caps of Settings bound only a task's chat.
type chatSpendCap struct {
	V              int    `json:"v"`
	ConversationID string `json:"conversationId"`
	// Bounded is false when nothing limits what a message spends.
	Bounded bool    `json:"bounded"`
	CapUSD  float64 `json:"capUsd"`
	Source  string  `json:"source,omitempty"` // message, session or task
	// TotalUSD is the session's budget or the task's cap that CapUSD is what
	// is left of.
	TotalUSD float64 `json:"totalUsd,omitempty"`
	// UsedUp: nothing is left under Source, so a message does not run.
	UsedUp bool   `json:"usedUp,omitempty"`
	TaskID string `json:"taskId,omitempty"`
	// UnenforceableOn names a runtime that can't hold a cap: the per-message
	// cap is dropped there, and a session's or task's cap refuses the turn.
	UnenforceableOn string `json:"unenforceableOn,omitempty"`
	// WorkersCapUSD is what a dynamic org's workers may spend per message on
	// top of CapUSD (a task's workers share its cap instead).
	WorkersCapUSD float64 `json:"workersCapUsd,omitempty"`
}

// lower keeps the lowest limit.
func (c *chatSpendCap) lower(source string, left, total float64) {
	if c.Bounded && c.CapUSD <= left {
		return
	}
	c.Bounded, c.Source, c.CapUSD, c.TotalUSD, c.UsedUp = true, source, left, total, left <= 0
}

func spendCapOfConversation(ctx context.Context, db *sql.DB, profileID string, conv ai.Conversation) (chatSpendCap, error) {
	out := chatSpendCap{V: 1, ConversationID: conv.ID}
	access := conv.EffectiveAccess()
	if access == ai.AccessScoped {
		return out, nil // only coder turns run under a cap
	}
	settings, err := loadCoderSettings(db)
	if err != nil {
		return out, err
	}
	settings = settings.forAccess(access)
	task, hasTask, err := (&aitask.Store{DB: db, Profile: profileID}).ForConversation(conv.ID)
	if err != nil {
		return out, err
	}
	// taskTurn's rule: an open task, or a confined one in any status.
	taskBound := hasTask && (task.Status == aitask.StatusRunning || task.Status == aitask.StatusNeedsUser || confinedKind(task.Kind))
	if taskBound {
		room, err := taskLedger(db).Room(ctx, budget.Admission{TaskID: task.ID, ConversationID: conv.ID, Kind: task.Kind})
		if err != nil {
			return out, err
		}
		out.TaskID = task.ID
		out.lower(spendCapTask, room.Left(), room.CapUSD)
	}
	sessions := coderhost.NewStore(db)
	total, err := sessions.Budget(profileID, conv.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	if total > 0 {
		left, capped, err := sessions.TurnCap(profileID, conv.ID)
		if err != nil && !capped { // capped with an error: the budget is used up
			return out, err
		}
		out.lower(spendCapSession, left, total)
	}
	enforceable := budget.Enforceable(conv.RuntimeID)
	// A runtime that can't hold a cap drops the per-message one, so it never
	// stands in front of a session's or task's cap, which refuses the turn.
	if settings.BudgetUSD > 0 && (enforceable || !out.Bounded) {
		out.lower(spendCapMessage, settings.BudgetUSD, 0)
	}
	if out.Bounded && !enforceable {
		out.UnenforceableOn = conv.RuntimeID
	}
	if !taskBound && conv.OrgMode == ai.OrgModeDynamic {
		out.WorkersCapUSD = settings.OrgBudgetUSD
	}
	return out, nil
}

func newChatHistorySpendCapCmd(cfg *Deps) *cobra.Command {
	return &cobra.Command{
		Use:   "spend-cap <conversation-id>",
		Short: "Show the spend cap the conversation's next message runs under",
		Long: "The lowest of what really bounds a coder conversation's turn: the coder budget per message " +
			"(`coder set --budget-usd`), what the session's budget has left (`coder sessions settings " +
			"--budget-usd`) and what an AI task's cap has left. A scoped chat has no cap.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, profileID, closeStore, err := openChatHistory(cfg)
			if err != nil {
				return err
			}
			defer closeStore()
			conv, err := store.GetConversation(args[0], profileID)
			if err != nil {
				return chatStoreErr(err)
			}
			db, err := cfg.OpenDB()
			if err != nil {
				return fmt.Errorf("initializing database: %w", err)
			}
			defer db.Close()
			out, err := spendCapOfConversation(cmd.Context(), db.DB, profileID, conv)
			if err != nil {
				return err
			}
			if cfg.JSONOutput() {
				return clicore.PrintJSON(out)
			}
			if !out.Bounded {
				fmt.Println("No spend cap bounds this conversation's messages.")
				return nil
			}
			fmt.Printf("next message: $%g (%s cap)\n", out.CapUSD, out.Source)
			if out.UnenforceableOn != "" {
				fmt.Printf("%s cannot enforce a spend cap\n", out.UnenforceableOn)
			}
			if out.WorkersCapUSD > 0 {
				fmt.Printf("org workers: up to $%g more per message\n", out.WorkersCapUSD)
			}
			return nil
		},
	}
}
