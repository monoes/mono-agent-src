package chat

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/aitask/budget"
	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/browserctl"
	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// taskTurn is the AI-task side of a turn in a coder conversation. When the
// conversation has an open task (running, or waiting for the user after its
// turn ended) and the caller is the operator, the turn is that task's next
// turn: it gets a fresh token (the old one stops working) bound to this
// process, in the environment as aitask.EnvTaskID and aitask.EnvToken, and the
// task goes back to running. end records how the turn ended: without
// `aitask complete` the task needs the user again.
//
// An agent, the chat's runs and workflow nodes (anything RequireOperator
// refuses) get nothing; neither does a conversation with no open task, a
// queued task (`aitask start` begins it) or another profile's. The first turn,
// which `aitask supervise` runs with the task's token already in its
// environment, keeps that token.
//
// A confined kind (confinedKind: application-apply, and the kinds that read
// untrusted text: draft-reply) is never a full-access coder turn, in the first turn or in
// any later one of its conversation, whatever the task's status.
//
// kind is the browser-permission kind the turn runs as: apply for the turn of
// an open application-apply task (the first turn and every follow-up), read
// only from the task row of this profile once the caller is the operator,
// else coder. Nothing an agent can set (a flag, the environment) picks it.
// An apply turn also comes back confined (chat_apply.go): it is never a
// full-access coder turn, so a task that cannot be confined (no snapshot of
// its context, no rebind) is an error, never a coder turn.
func taskTurn(cfg *Deps, conversationID string, turnCap *float64) (run taskRun) {
	run.end = func(error) {}
	run.kind = browserctl.KindCoder
	db, err := cfg.OpenDB()
	if err != nil {
		if clicore.RequireOperator(cfg.RequireOperator, "continue an AI task's turn") != nil {
			return run
		}
		run.err = fmt.Errorf("looking up the conversation's task: %w", err)
		return run
	}
	defer db.Close()
	store := &aitask.Store{DB: db.DB, Profile: cfg.ProfileID()}
	// Confinement belongs to the conversation, not to the task's status: the
	// newest task in any status decides, so an application-apply conversation
	// stays confined after its task is complete, failed or cancelled.
	t, ok, err := store.ForConversation(conversationID)
	if err != nil {
		run.err = fmt.Errorf("looking up the conversation's task: %w", err)
		return run
	}
	if !ok {
		return run
	}
	apply := t.Kind == applyfill.Kind
	confined := confinedKind(t.Kind)
	operator := clicore.RequireOperator(cfg.RequireOperator, "continue an AI task's turn") == nil
	if !operator {
		if confined {
			run.err = clicore.OperatorOnly("only the user can continue a %s task's conversation", t.Kind)
		}
		return run
	}
	if confined {
		if apply || aitask.IsExtensionKind(t.Kind) {
			run.kind = browserctl.KindApply // the browser-capable confined kinds
		}
		if run.confine, err = confineTurn(cfg, t.Kind, t.ID); err != nil {
			run.kind, run.err = browserctl.KindCoder, err
			return run
		}
		// The task id (never the token: CheckToken still guards complete and fail)
		// marks every confined turn, open or over, so the CLI's task-session guard
		// (cmd/monoagentcli/task_session_guard.go) holds the agent to its profile.
		// This env var is the guard's single signal: every path that returns a
		// confinement carries it (TestEveryConfinedTurnCarriesTheTaskID).
		run.env = map[string]string{aitask.EnvTaskID: t.ID}
	}
	open := t.Status == aitask.StatusRunning || t.Status == aitask.StatusNeedsUser
	if !open && !confined {
		return run // an ordinary coder turn after a generic task is over
	}
	run.end = func(turnErr error) {
		text := ""
		if turnErr != nil {
			text = "the agent's turn ended with an error: " + turnErr.Error()
		}
		if db, err := cfg.OpenDB(); err == nil {
			defer db.Close()
			if open {
				_ = (&aitask.Service{Store: &aitask.Store{DB: db.DB, Profile: cfg.ProfileID()}}).EndTurn(t.ID, text)
			}
			// A follow-up turn's spend counts against the same caps as the first.
			l := taskLedger(db.DB)
			_ = l.Settle(context.Background(), t.ID, t.ConversationID)
		}
	}
	if open && os.Getenv(aitask.EnvTaskID) == t.ID && store.CheckToken(t.ID, os.Getenv(aitask.EnvToken)) {
		return run
	}
	// An explicit cap is the turn's own spend limit: 0, negative or not finite
	// is no spend, never "no limit" (absent is what asks for none).
	asked := 0.0
	if turnCap != nil {
		if !validTurnCap(*turnCap) {
			run.kind, run.confine, run.end, run.err = browserctl.KindCoder, nil, func(error) {}, noBudgetError(*turnCap)
			return run
		}
		asked = *turnCap
	}
	// A follow-up is admitted again, against what is left of the task's cap and
	// the turn's own, before it gets a token: a task with nothing left gets no turn.
	l := taskLedger(db.DB)
	grant, err := l.AdmitTurn(context.Background(), budget.Admission{TaskID: t.ID, ConversationID: t.ConversationID, Kind: t.Kind, Runtime: t.Runtime}, asked)
	if err != nil {
		run.kind, run.confine, run.end, run.err = browserctl.KindCoder, nil, func(error) {}, err
		return run
	}
	run.budget = &grant
	if !open {
		return run // a finished task never gets a token
	}
	token := aitask.NewToken()
	if moved, err := store.Rebind(t.ID, token, os.Getpid()); err != nil || !moved {
		_ = l.Settle(context.Background(), t.ID, t.ConversationID)
		run.budget = nil
		if confined {
			run.err = fmt.Errorf("task %s could not be handed to this turn", t.ID)
		}
		run.kind, run.confine, run.end = browserctl.KindCoder, nil, func(error) {}
		return run
	}
	run.env = map[string]string{aitask.EnvTaskID: t.ID, aitask.EnvToken: token}
	return run
}

// taskRun is what taskTurn found for a turn.
type taskRun struct {
	env     map[string]string
	kind    string // the browser-permission kind
	confine *confinement
	end     func(error)
	err     error
	// budget is the cap the turn runs under, set for a follow-up turn.
	budget *float64
}

// taskLedger is the budget ledger over db, bounded by the coder budget.
func taskLedger(db *sql.DB) *budget.Ledger {
	l := &budget.Ledger{DB: db}
	if st, err := loadCoderSettings(db); err == nil {
		l.Operator = st.BudgetUSD
	}
	return l
}

// validTurnCap says whether an explicit --budget-usd is a real spend limit.
func validTurnCap(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func noBudgetError(v float64) error {
	return &coderError{code: "no_budget", err: &clicore.Error{Code: 3,
		Msg: fmt.Sprintf("--budget-usd %v is not a spend limit: the turn was not run (0 is no spend, never unlimited; leave the flag out for none)", v)}}
}
