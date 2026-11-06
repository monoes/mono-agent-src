package doomsday

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/daemonhb"
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
	// Lock takes the profile's start lock without waiting and returns its release; a second start gets errStartBusy.
	Lock(ctx context.Context) (release func(), err error)
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

// errStartBusy is what a start gets while another start of this profile holds the start lock.
var errStartBusy = errors.New("a start is already in progress for this profile: wait for it, then look at the Control room; nothing was made")

// cleanupTimeout bounds the cleanup of a failed start, which runs on a context of its own: a cancelled CLI must still end
// the boss it made.
const cleanupTimeout = 30 * time.Second

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
	release, err := cs.Lock(ctx)
	if err != nil {
		if errors.Is(err, daemonhb.ErrHeld) {
			return conflictError{&clicore.Error{Code: 3, Msg: errStartBusy.Error()}}
		}
		return fmt.Errorf("the start lock could not be taken, so nothing was started: %w", err)
	}
	defer release()
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
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if eerr := cs.EndBoss(cctx, ref.SessionID); eerr != nil {
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
	// A Stop pressed before this point finds no run to stop: the entry below still happens (the person pressed Stop for a
	// start that was not a run yet). The Control room shows the run as soon as it exists.
	entry, err := e.Enter(ctx, canon, canon.Hash, ack)
	if err != nil {
		return afterFailedEnter(ctx, e, canon.Hash, err, undo)
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

// afterFailedEnter decides what a failed Enter leaves. The run's existence is read again: not readable is "unknown" (the boss
// stays, the answer says the run may exist); no run is a plain refusal (the boss is ended); a run of THIS charter is the run
// this start made (the boss stays, a Stop ends it); a run of another charter is not ours (the boss is ended, the person is told).
func afterFailedEnter(ctx context.Context, e Engine, hash string, enterErr error, undo func(error) error) error {
	now, serr := e.Status(ctx)
	switch {
	case serr != nil:
		return fmt.Errorf("the run may have started but its state could not be read after the entry failed (%v): check the Control room and press Stop if it is running: %w", serr, enterErr)
	case !now.Active:
		return undo(enterErr)
	case now.Hash != "" && strings.HasPrefix(hash, now.Hash):
		return enterErr
	default:
		return undo(runExistsError(now.RunID))
	}
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
