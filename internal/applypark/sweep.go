package applypark

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
)

// ReopenNote is what the application page shows when its tab is gone.
const ReopenNote = "Reopen and refill: the browser tab with the filled form was closed."

// ResumePending prefixes the task id a resume writes into the tab before its task exists: no task matches it, so
// nothing can act on the tab while the new task is created. NewResumeHold makes each resume's own.
const ResumePending = "resume-pending"

// FillTaskKind is the kind of task a resume starts (applyfill.Kind; a test there holds the two together).
const FillTaskKind = "application-apply"

// NewResumeHold returns the placeholder one resume holds a tab with. It is unique, so a late write by that resume
// can tell the tab still is its own.
func NewResumeHold() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return ResumePending + ":" + hex.EncodeToString(b)
}

// IsResumeHold says whether a tab's task id is a resume's placeholder, not a task.
func IsResumeHold(taskID string) bool {
	return taskID == ResumePending || strings.HasPrefix(taskID, ResumePending+":")
}

// ResumeHoldTTL is how long a resume hold is taken to be a resume still in flight. Older than that, the resume
// is taken to have died between its two state writes and Sweep gives the tab back; a resume slower than this
// loses its tab (Store.Rebind says so).
const ResumeHoldTTL = 10 * time.Minute

// SweepResult says what a sweep changed.
type SweepResult struct {
	Closed []string `json:"closed"` // applications that are over: their tab binding is closed
	Lost   []string `json:"lost"`   // applications whose tab is gone: now needs_you
}

// Sweep reconciles the live bindings against the browser's tabs. An
// application that is no longer being worked on (applied, rejected,
// cancelled) has its binding closed. A binding whose tab is not in live
// becomes lost and its application needs_you with ReopenNote (a new task
// refills it). live must be a successful read of `browser tabs`: with the
// browser unreachable do not sweep, nothing is known. Sweep never sets
// applied.
func Sweep(ctx context.Context, store Store, apps *applications.Store, live []LiveTab) (SweepResult, error) {
	res := SweepResult{Closed: []string{}, Lost: []string{}}
	open := map[int]bool{}
	for _, t := range live {
		open[t.TabID] = true
	}
	tabs, err := store.Live(ctx)
	if err != nil {
		return res, err
	}
	for _, t := range tabs {
		app, err := apps.Get(ctx, store.Profile, t.ApplicationID)
		if err != nil {
			return res, err
		}
		switch app.Status {
		case applications.StatusAwaitingSubmit, applications.StatusNeedsYou, applications.StatusPending:
		default:
			if err := store.SetState(ctx, t.ApplicationID, StateClosed, ""); err != nil {
				return res, err
			}
			res.Closed = append(res.Closed, t.ApplicationID)
			continue
		}
		if open[t.TabID] {
			if err := parkIfTaskEnded(ctx, store, t); err != nil {
				return res, err
			}
			if err := recoverStaleResume(ctx, store, t); err != nil {
				return res, err
			}
			continue
		}
		if err := store.SetState(ctx, t.ApplicationID, StateLost, ""); err != nil {
			return res, err
		}
		if err := markReopen(ctx, apps, store.Profile, app); err != nil {
			return res, fmt.Errorf("application %s: %w", t.ApplicationID, err)
		}
		res.Lost = append(res.Lost, t.ApplicationID)
	}
	return res, nil
}

// parkIfTaskEnded gives an `active` tab back to the person when the task that held it is over (cancelled with
// its conversation's delete, failed, or done without parking it): nothing else would, the guard refusing every
// other process and a resume taking only parked or lost tabs. A task with no row (the placeholder a resume
// writes before its task exists) or in another profile is not judged.
func parkIfTaskEnded(ctx context.Context, store Store, t Tab) error {
	if t.State != StateActive || t.TaskID == "" {
		return nil
	}
	// One statement: the tab is parked only while it still is the one this sweep read (active under the same
	// task) and that task still is over, so a task that took the tab since the read keeps it.
	_, err := store.DB.ExecContext(ctx,
		`UPDATE application_tabs SET state = ?, updated_at = ?
		 WHERE application_id = ? AND state = ? AND task_id = ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)
		   AND EXISTS (SELECT 1 FROM ai_tasks k WHERE k.id = application_tabs.task_id AND k.profile_id = ? AND k.status IN (?, ?, ?))`,
		string(StateParked), now(), t.ApplicationID, string(StateActive), t.TaskID, store.Profile,
		store.Profile, aitask.StatusComplete, aitask.StatusFailed, aitask.StatusCancelled)
	return err
}

// recoverStaleResume settles a tab a resume left under its hold past ResumeHoldTTL, in one statement that holds
// only while the tab still is that stale hold: bound to the task that resume started (an application-fill task of
// the application created since the hold was written) when there is one, else parked for the person. An older
// task of the application is not this resume's and is never taken for it.
func recoverStaleResume(ctx context.Context, store Store, t Tab) error {
	if t.State != StateActive || !IsResumeHold(t.TaskID) {
		return nil
	}
	cutoff := time.Now().UTC().Add(-ResumeHoldTTL).Format(time.RFC3339)
	const started = `SELECT k.id FROM ai_tasks k WHERE k.application_id = application_tabs.application_id AND k.profile_id = ?
		   AND k.kind = ? AND k.status NOT IN (?, ?, ?) AND julianday(k.created_at) >= julianday(application_tabs.updated_at)
		 ORDER BY julianday(k.created_at) DESC, k.rowid DESC LIMIT 1`
	args := []any{store.Profile, FillTaskKind, aitask.StatusComplete, aitask.StatusFailed, aitask.StatusCancelled}
	all := append(append([]any{}, args...), args...)
	all = append(all, string(StateActive), string(StateParked), now(), t.ApplicationID, string(StateActive), t.TaskID, cutoff, store.Profile)
	_, err := store.DB.ExecContext(ctx,
		`UPDATE application_tabs SET task_id = COALESCE((`+started+`), task_id),
		   state = CASE WHEN EXISTS (`+started+`) THEN ? ELSE ? END, updated_at = ?
		 WHERE application_id = ? AND state = ? AND task_id = ? AND updated_at < ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)`, all...)
	return err
}

// markReopen turns the application into needs_you with ReopenNote, keeping its
// field report.
func markReopen(ctx context.Context, apps *applications.Store, profile string, app *applications.Application) error {
	fill, err := apps.GetFill(ctx, profile, app.ID)
	if err != nil {
		return err
	}
	fill.Status, fill.Note = applications.FillNeedsYou, ReopenNote
	if err := apps.RecordFill(ctx, profile, app.ID, fill); err != nil {
		return err
	}
	if app.Status == applications.StatusNeedsYou {
		return nil
	}
	return apps.SetStatus(ctx, profile, app.ID, applications.StatusNeedsYou, applications.ActorSystem, ReopenNote)
}
