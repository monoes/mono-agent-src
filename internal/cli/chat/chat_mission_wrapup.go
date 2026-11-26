package chat

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/dynorg"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/storage"
	"github.com/monoes/mono-agent/internal/tasks"
)

// noticeMissionWrapUp says a turn runs under its mission's wrap-up allowance, and what became of it.
const noticeMissionWrapUp = "mission.wrapup"

// wrapUpTurn is a turn of a mission's lead admitted under the wrap-up allowance (tasks.WrapUp, #1751). It holds what
// is left of the allowance from before the runtime starts until the turn's cost is known; the cost then goes on the
// mission's meter and against the allowance. The org's conductor charges nothing for such a turn.
type wrapUpTurn struct {
	db      *storage.Database
	store   *tasks.Store
	journal *turnJournal
	session tasks.Session
	grant   tasks.WrapUp // as it was before this turn's hold: LeftUSD is what the turn may use
	ran     bool         // the runtime reported something besides its end: it may have spent
	decided bool         // settled, given back or left held on purpose
}

// wrapUpOperator records the allowance: the chat's own process does, as it records the lead's spend.
var wrapUpOperator = tasks.Actor{Kind: tasks.Human}

// beginWrapUp admits the turn under the wrap-up allowance when the conversation's mission has spent its budget: nil
// when it has not (also an unlimited budget, an operator's pause or stop). With the allowance used up it refuses the
// turn, naming how to raise the budget.
func beginWrapUp(ctx context.Context, cfg *Deps, journal *turnJournal, t coderTurn) (*wrapUpTurn, error) {
	db, err := cfg.OpenDB()
	if err != nil {
		return nil, fmt.Errorf("initializing database: %w", err)
	}
	w, err := admitWrapUp(ctx, db, journal, t)
	if w == nil {
		db.Close()
	}
	return w, err
}

func admitWrapUp(ctx context.Context, db *storage.Database, journal *turnJournal, t coderTurn) (*wrapUpTurn, error) {
	mission := missionConfig(ctx, db.DB, journal.profileID, journal.conversationID, t)
	se, _, err := mission.WrapUp(ctx)
	if err != nil {
		return nil, fmt.Errorf("this message was not run: the mission's budget could not be read: %w", err)
	}
	if se == nil {
		return nil, nil
	}
	grant, err := mission.Store.BeginWrapUpTurn(ctx, journal.profileID, se.ID, journal.turnID, wrapUpOperator)
	switch {
	case errors.Is(err, tasks.ErrWrapUpUsedUp) && grant != nil:
		return nil, wrapUpUsedUp(journal.profileID, *se, *grant)
	case err != nil:
		return nil, fmt.Errorf("this message was not run: the mission's wrap-up allowance could not be held: %w", err)
	case grant == nil:
		return nil, nil
	}
	return &wrapUpTurn{db: db, store: mission.Store, journal: journal, session: *se, grant: *grant}, nil
}

// wrapUpUsedUp refuses a turn once nothing is left of the allowance.
func wrapUpUsedUp(profile string, se tasks.Session, w tasks.WrapUp) error {
	used := fmt.Sprintf("$%.2f spent on wrap-up turns", w.SpentUSD)
	if w.HeldUSD > 0 {
		used += fmt.Sprintf(", $%.2f held by a turn that is still running or reported no cost", w.HeldUSD)
	}
	cli := "monoagentcli --profile " + profile + " task session"
	return &coderError{code: "mission_wrapup_used", err: &clicore.Error{Code: 3, Msg: fmt.Sprintf(
		"This message was not run: mission #%d %q has spent its budget ($%.2f of $%.2f) and the lead's $%.2f wrap-up allowance is used up (%s). "+
			"To go on, raise the budget above $%.2f with `%s budget %d --usd AMOUNT`, then run a paused mission again with `%s resume %d`. "+
			"To end it instead: `%s stop %d`.",
		se.ID, se.Title, se.SpentUSD, se.BudgetUSD, w.AllowanceUSD, used, se.SpentUSD, cli, se.ID, cli, se.ID, cli, se.ID)}}
}

// wrapUpCapHolds says whether --budget-usd can hold a turn to an amount of its own. monomind compares it with the
// cost the runtime reports, which for a resumed claude session is the whole session's total (#1744), so a healthy
// turn would fail; kilo takes no cap.
func wrapUpCapHolds(runtime, resume string) bool {
	return runtime != "kilo" && (runtime != "claude" || resume == "")
}

// capWrapUpTurn caps the turn at what is left of the allowance where the runtime can hold that, keeping a lower cap
// the turn already has. left is above 0: --budget-usd 0 would be no cap at all.
func capWrapUpTurn(opts *monomind.ExecOptions, left float64) bool {
	if left <= 0 || !wrapUpCapHolds(opts.Runtime, opts.Resume) {
		return false
	}
	if opts.BudgetUSD <= 0 || left < opts.BudgetUSD {
		opts.BudgetUSD = left
	}
	return true
}

// apply tells the lead where the mission stands and caps the turn.
func (w *wrapUpTurn) apply(opts *monomind.ExecOptions) {
	opts.SystemPrompt += dynorg.WrapUpPrompt(w.journal.profileID, w.session, w.grant)
	note := fmt.Sprintf("Mission #%d has spent its budget: this message runs under the lead's wrap-up allowance ($%.2f of $%.2f left). No worker starts.",
		w.session.ID, w.grant.LeftUSD, w.grant.AllowanceUSD)
	if !capWrapUpTurn(opts, w.grant.LeftUSD) {
		note += " This runtime session takes no cap of its own, so the allowance is checked when a message starts: this one may go past it."
	}
	w.journal.notice(noticeMissionWrapUp, note, chatevents.SeverityWarning)
}

// watch passes the runtime's events on, noting that it ran.
func (w *wrapUpTurn) watch(next func(monomind.Event)) func(monomind.Event) {
	return func(ev monomind.Event) {
		if ev.Type != monomind.EventError && ev.Type != monomind.EventDone {
			w.ran = true
		}
		next(ev)
	}
}

// settle records what the turn cost. A turn that reported no cost keeps all it held, unless it never reached a model.
func (w *wrapUpTurn) settle(ctx context.Context, res *monomind.TurnResult) {
	ctx = context.WithoutCancel(ctx)
	usd, reported := leadTurnFigure(w.db.DB, w.journal, res)
	w.decided = true
	var err error
	switch {
	case reported:
		err = w.store.SettleWrapUpTurn(ctx, w.journal.profileID, w.session.ID, w.journal.turnID, usd, wrapUpOperator)
	case !w.ran || neverReachedModel(res):
		err = w.drop(ctx)
	default:
		w.journal.notice(noticeMissionWrapUp, fmt.Sprintf("This turn reported no cost, so all it was admitted with ($%.2f) counts against the mission's wrap-up allowance.", w.grant.LeftUSD), chatevents.SeverityWarning)
		return
	}
	if err != nil {
		w.journal.notice(noticeMissionWrapUp, fmt.Sprintf("The turn's cost ($%.4f) could not be recorded, so what it held still counts against the wrap-up allowance: %v", usd, err), chatevents.SeverityWarning)
		return
	}
	if now, err := w.store.WrapUpOf(ctx, w.journal.profileID, w.session.ID, wrapUpOperator); err == nil && now != nil {
		note := fmt.Sprintf("Wrap-up allowance of mission #%d: $%.2f of $%.2f left.", w.session.ID, now.LeftUSD, now.AllowanceUSD)
		if now.LeftUSD <= 0 {
			note = fmt.Sprintf("The wrap-up allowance of mission #%d is used up: the next message is refused until you raise the budget (task session budget %d --usd AMOUNT, then task session resume %d).",
				w.session.ID, w.session.ID, w.session.ID)
		}
		w.journal.notice(noticeMissionWrapUp, note, chatevents.SeverityInfo)
	}
}

// neverReachedModel says whether the turn failed in a way that spends nothing.
func neverReachedModel(res *monomind.TurnResult) bool {
	if res == nil || res.Err == nil {
		return false
	}
	switch res.Err.Code {
	case monomind.ErrAuth, monomind.ErrMissingBinary, monomind.ErrNoRunner, monomind.ErrUnsupported:
		return true
	}
	return false
}

func (w *wrapUpTurn) drop(ctx context.Context) error {
	return w.store.DropWrapUpTurn(ctx, w.journal.profileID, w.session.ID, w.journal.turnID, wrapUpOperator)
}

// close gives the hold back when the turn ended before its runtime started, and releases the database.
func (w *wrapUpTurn) close(ctx context.Context) {
	if !w.decided && !w.ran {
		_ = w.drop(context.WithoutCancel(ctx))
	}
	w.db.Close()
}

// warnContext is the context warning of a turn that is not compacted: a compaction would be a paid call after the
// allowance was counted.
func (w *wrapUpTurn) warnContext() {
	w.journal.mu.Lock()
	peak := w.journal.contextPeak
	w.journal.mu.Unlock()
	if peak > 0 {
		w.journal.notice(noticeCoderResult, contextWarningText(peak), chatevents.SeverityWarning)
	}
}
