package doomsday

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
)

// The start from a chat (`start --chat CHAT`, the app's path). One process makes the boss, gives it the charter's budget,
// checks it, enters the run, records the boss and queues the boss's first turn; each step records what it made, so a later
// failure undoes exactly that and nothing else:
//
//	run exists?        -> refused, nothing made
//	make boss          -> (atomic) nothing left on failure
//	boss budget        -> failure: end the boss
//	check boss         -> failure: end the boss
//	enter              -> failure: end the boss, unless a run now exists (then the boss stays: the run needs it, and Stop ends it)
//	record boss        -> failure: the run exists; the boss stays; the answer says to Stop
//	first turn         -> failure: NOT an error: the run started, the answer says the boss has no first turn
//
// A boss left unstamped by a start that died is found again by MakeBoss (it reuses the chat's unstamped boss), so a repeated
// start never makes a second one, and every refused start ends the boss it used.

// FirstTurn is what the boss is told in its first turn.
type FirstTurn struct {
	RunID       string
	CharterHash string
	Goal        string
}

// ChatStarter is what the start from a chat needs beside the engine; the CLI wires the real one (DBChatStart).
type ChatStarter interface {
	// MakeBoss makes the chat's boss session, or returns the unstamped one that already stands behind it.
	MakeBoss(ctx context.Context, chat, goal string) (BossRef, error)
	// SetBossBudget sets the boss's budget in dollars; 0 is "no ceiling of its own".
	SetBossBudget(ctx context.Context, session int64, usd float64) error
	// EndBoss ends the boss session by the guarded paths (stop, then archive); ending one that is ended is not an error.
	EndBoss(ctx context.Context, session int64) error
	// QueueFirstTurn queues the boss's first turn for the boss's conversation.
	QueueFirstTurn(ctx context.Context, bossConversation string, in FirstTurn) error
}

// ChatEntry is the answer of a start from a chat: the entry, the boss, and the state of the boss's first turn.
type ChatEntry struct {
	Entry
	BossSession      int64  `json:"boss_session"`
	BossConversation string `json:"boss_conversation"`
	FirstTurn        string `json:"first_turn"` // "queued" or "failed"
	FirstTurnError   string `json:"first_turn_error,omitempty"`
}

// conflictError is the refusal of a second run: exit 3, code "conflict".
type conflictError struct{ err *clicore.Error }

func (e conflictError) Error() string                 { return e.err.Msg }
func (e conflictError) Unwrap() error                 { return e.err }
func (conflictError) JSONErrorFields() map[string]any { return map[string]any{"code": "conflict"} }

func runExistsError(runID string) error {
	msg := "a run already exists for this profile: only one open-ended run goes at a time; open the Control room and stop it first, nothing was made"
	if runID != "" {
		msg = "a run already exists for this profile (" + runID + "): only one open-ended run goes at a time; open the Control room and stop it first, nothing was made"
	}
	return conflictError{&clicore.Error{Code: 3, Msg: msg}}
}

// startFromChat runs the steps after the phrase was checked. e must be able to check and record a boss.
func (d Deps) startFromChat(cmd *cobra.Command, e Engine, ch Charter, canon Canon, ack Ack, chat string) error {
	ctx := cmd.Context()
	var cs ChatStarter
	if d.ChatStart != nil {
		cs = d.ChatStart()
	}
	bc, _ := e.(BossChecker)
	bb, _ := e.(BossBinder)
	if cs == nil || bc == nil || bb == nil {
		return clicore.InvalidInput("this install cannot start from a chat, so nothing was started")
	}
	st, err := e.Status(ctx)
	if err != nil {
		return fmt.Errorf("the profile's open-ended run cannot be read, so nothing was started: %w", err)
	}
	if st.Active {
		return runExistsError(st.RunID)
	}
	ref, err := cs.MakeBoss(ctx, chat, ch.Goal)
	if err != nil {
		return err
	}
	undo := func(cause error) error {
		if eerr := cs.EndBoss(ctx, ref.SessionID); eerr != nil {
			return fmt.Errorf("%w (the boss session %d could not be ended: %v; end it from the Agents page)", cause, ref.SessionID, eerr)
		}
		return cause
	}
	if err := cs.SetBossBudget(ctx, ref.SessionID, float64(ch.Limits.BudgetMicro)/1e6); err != nil {
		return undo(fmt.Errorf("the boss's budget could not be set, so nothing was started: %w", err))
	}
	if err := bc.CheckBoss(ctx, ref.SessionID); err != nil {
		return undo(clicore.InvalidInput("%v", err))
	}
	entry, err := e.Enter(ctx, canon, canon.Hash, ack)
	if err != nil {
		// A failure after the run was recorded leaves a run that needs its boss: keep it. Anything else made no run.
		if now, serr := e.Status(ctx); serr == nil && !now.Active {
			return undo(err)
		}
		return err
	}
	if err := bb.BindBoss(ctx, ref.SessionID, entry); err != nil {
		return fmt.Errorf("run %s started but session %d could not be recorded as its boss, so no boss tool works (run `doomsday stop`): %w", entry.RunID, ref.SessionID, err)
	}
	res := ChatEntry{Entry: entry, BossSession: ref.SessionID, BossConversation: ref.ConversationID, FirstTurn: "queued"}
	if err := cs.QueueFirstTurn(ctx, ref.ConversationID, FirstTurn{RunID: entry.RunID, CharterHash: canon.Hash, Goal: ch.Goal}); err != nil {
		res.FirstTurn, res.FirstTurnError = "failed", cleanLine(err.Error())
	}
	return d.printChatEntry(cmd.OutOrStdout(), res)
}

// printChatEntry is the answer of a start from a chat.
func (d Deps) printChatEntry(w io.Writer, r ChatEntry) error {
	if d.json() {
		return json.NewEncoder(w).Encode(r)
	}
	fmt.Fprintf(w, "run %s started (charter %s, generation %d); boss session %d\n", r.RunID, short(r.Hash), r.Generation, r.BossSession)
	if r.FirstTurn == "failed" {
		fmt.Fprintf(w, "the boss has no first turn: %s (steer it from the chat, or press Stop)\n", r.FirstTurnError)
	}
	return nil
}
