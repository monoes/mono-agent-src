// wails-app/app_tasks_watch.go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// restartTaskWatcher stops the running task board watcher and starts one on
// the active profile's board, at startup and on SwitchProfile (shutdown
// calls stopTaskWatcher), as restartDocumentWatcher does. The watcher reads
// the board revision, a primary-key read, every two seconds, and the counts
// when it moves (spec D12); tasks:changed {profile_id, rev, inbox, review}
// (plus drafts, the session plans awaiting approval) feeds the sidebar badge and makes the Tasks page read the board again.
func (a *App) restartTaskWatcher() {
	a.taskWatchMu.Lock()
	defer a.taskWatchMu.Unlock()
	if a.taskWatchStop != nil {
		a.taskWatchStop()
		a.taskWatchStop = nil
	}
	a.taskWatchProfile = ""
	if a.db == nil {
		return
	}
	profileID, err := a.profileID()
	if err != nil {
		return // no profile selected: no board to watch
	}
	a.taskWatchStop = a.startTaskWatcher(profileID, 0, a.emitFolderEvent)
	a.taskWatchProfile = profileID
}

// stopTaskWatcher ends the watcher for good (shutdown).
func (a *App) stopTaskWatcher() {
	a.taskWatchMu.Lock()
	defer a.taskWatchMu.Unlock()
	if a.taskWatchStop != nil {
		a.taskWatchStop()
		a.taskWatchStop = nil
	}
	a.taskWatchProfile = ""
}

// startTaskWatcher watches profileID's board revision (interval <= 0 is the
// store's two seconds) and emits tasks:changed at the start and at each move.
// stop returns only once the watcher's goroutine has ended, and a report
// that races the stop is dropped, so no event of the old profile follows a
// restart. Watch returns an error only for a profile that is not there (it
// never was, or it was deleted while watched): the watcher then writes one
// warning to the app's log and ends. It does not start over, since the
// profile would still be missing; the next SwitchProfile or start begins a
// new one.
func (a *App) startTaskWatcher(profileID string, interval time.Duration, emit folderEventFunc) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	store := tasks.NewStore(a.db)
	go func() {
		defer close(done)
		err := store.Watch(ctx, profileID, interval, func(c tasks.Change) {
			if ctx.Err() != nil {
				return
			}
			pulse := taskPulseData(profileID, c)
			pulse["drafts"] = draftSessions(ctx, store, profileID)
			emit("tasks:changed", pulse)
		})
		if err != nil {
			a.emitLog("SYSTEM", "WARN", fmt.Sprintf("profile %s: task board watcher stopped: %v", profileID, err))
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// taskPulseData is the tasks:changed payload and TaskPulse's answer.
func taskPulseData(profileID string, c tasks.Change) map[string]interface{} {
	return map[string]interface{}{
		"profile_id": profileID,
		"rev":        c.Rev,
		"inbox":      c.Counts.Inbox,
		"review":     c.Counts.Review,
	}
}

// TaskPulse is the active profile's board revision with its Inbox and Review
// counts, read now in process the way the watcher reads them. The sidebar
// asks once when it mounts: the watcher's first tasks:changed fires during
// startup, before any page listens. {} without a database, for a profile
// that is not there, or on a failed read.
func (a *App) TaskPulse() map[string]interface{} {
	if a.signedOut() != nil {
		return nil
	}
	if a.db == nil {
		return map[string]interface{}{}
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	profileID, err := a.profileID()
	if err != nil {
		return map[string]interface{}{}
	}
	store := tasks.NewStore(a.db)
	// Rev and Counts do not check the profile: an unknown one reads revision 0
	// and no cards, a board that never existed. Resolve it first, as Watch does.
	if _, err := store.Profile(ctx, profileID); err != nil {
		return map[string]interface{}{}
	}
	rev, err := store.Rev(ctx, profileID)
	if err != nil {
		return map[string]interface{}{}
	}
	counts, err := store.Counts(ctx, profileID)
	if err != nil {
		return map[string]interface{}{}
	}
	pulse := taskPulseData(profileID, tasks.Change{Rev: rev, Counts: counts})
	pulse["drafts"] = draftSessions(ctx, store, profileID)
	return pulse
}

// TaskSessionPulse is the session's revision and state, read now in process:
// the page reads the board again when the revision moves. {} without a
// database or for a session that is not there.
func (a *App) TaskSessionPulse(id int64) map[string]interface{} {
	if a.signedOut() != nil {
		return nil
	}
	if a.db == nil || id <= 0 {
		return map[string]interface{}{}
	}
	ctx := a.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	pid, err := a.profileID()
	if err != nil {
		return map[string]interface{}{}
	}
	se, err := tasks.NewStore(a.db).GetSession(ctx, pid, id, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		return map[string]interface{}{}
	}
	return map[string]interface{}{"session_id": se.ID, "rev": se.Rev, "state": string(se.State)}
}

// draftSessions counts the profile's drafts awaiting approval: the plans the
// sidebar badge adds to Inbox and Review. 0 on a failed read.
func draftSessions(ctx context.Context, store *tasks.Store, profileID string) int {
	ss, err := store.ListSessions(ctx, profileID, tasks.SessionFilter{State: tasks.SessionDraft}, tasks.Actor{Kind: tasks.Human})
	if err != nil {
		return 0
	}
	return len(ss)
}
