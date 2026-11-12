package task

import (
	"context"
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/tasks"
)

// overCommitOf reads whether the session, or a budgeted ancestor of it, is committed past its budget (read only). A read
// that fails shows as "not over": the run itself is refused regardless, and a status command should still answer.
func overCommitOf(ctx context.Context, store *tasks.Store, profileID string, session int64) tasks.OverCommit {
	oc, err := store.OverCommitOf(ctx, profileID, session)
	if err != nil {
		return tasks.OverCommit{Session: session}
	}
	return oc
}

// overCommitLive says whether a session can still be run, so only those are worth looking at in a list.
func overCommitLive(se tasks.Session) bool {
	return !se.Archived && (se.State == tasks.SessionRunning || se.State == tasks.SessionPaused || se.State == tasks.SessionDraft)
}

func printOverCommit(w io.Writer, oc tasks.OverCommit) {
	if oc.Over {
		fmt.Fprintf(w, "Budget:   %s\n", oc.Words())
	}
}
