package task

import (
	"context"
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/tasks"
)

// A mission that has spent its budget keeps a wrap-up allowance for its lead (tasks.WrapUp): the commands that show
// a session's budget show it too.

// wrapUpOf is the session's wrap-up allowance, nil when none applies. Like overCommitOf, a read that fails shows as
// none: the turn itself checks again, and a status command should still answer.
func wrapUpOf(ctx context.Context, store *tasks.Store, profileID string, se tasks.Session, actor tasks.Actor) *tasks.WrapUp {
	if se.BudgetUSD <= 0 || se.SpentUSD < se.BudgetUSD {
		return nil // not spent: nothing to read
	}
	w, err := store.WrapUpOf(ctx, profileID, se.ID, actor)
	if err != nil {
		return nil
	}
	return w
}

// wrapUpWords is the allowance in a line.
func wrapUpWords(w tasks.WrapUp) string {
	s := fmt.Sprintf("%s of %s left", money(w.LeftUSD), money(w.AllowanceUSD))
	if w.HeldUSD > 0 {
		s += fmt.Sprintf(" (%s held by a turn that is running or reported no cost)", money(w.HeldUSD))
	}
	return s
}

func printWrapUp(w io.Writer, se tasks.Session, wu *tasks.WrapUp) {
	if wu != nil {
		fmt.Fprintf(w, "Wrap-up:  budget used up; the lead's wrap-up allowance: %s. Raise the budget with `task session budget %d --usd AMOUNT` to go on\n",
			wrapUpWords(*wu), se.ID)
	}
}
