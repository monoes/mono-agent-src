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

// overCommitOfLive reads the over-committed state of every live session of a list in one pass (not one subtree walk per
// session), as the map by session and the list in the list's order. A read that fails shows as "not over", as above.
func overCommitOfLive(ctx context.Context, store *tasks.Store, profileID string, ss []tasks.Session) (map[int64]tasks.OverCommit, []tasks.OverCommit) {
	over := map[int64]tasks.OverCommit{}
	overList := []tasks.OverCommit{}
	var live []int64
	for _, se := range ss {
		if overCommitLive(se) {
			live = append(live, se.ID)
		}
	}
	if len(live) == 0 {
		return over, overList
	}
	all, err := store.OverCommitAll(ctx, profileID, live)
	if err != nil {
		return over, overList
	}
	for _, id := range live {
		if oc := all[id]; oc.Over {
			over[id] = oc
			overList = append(overList, oc)
		}
	}
	return over, overList
}

func printOverCommit(w io.Writer, oc tasks.OverCommit) {
	if oc.Over {
		fmt.Fprintf(w, "Budget:   %s\n", oc.Words())
	}
}
