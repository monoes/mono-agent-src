package applypark

import (
	"context"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/aitask"
	"github.com/monoes/mono-agent/internal/applications"
)

// ReopenNote is what the application page shows when its tab is gone.
const ReopenNote = "Reopen and refill: the browser tab with the filled form was closed."

// ResumeHoldTTL is how long a tab held under a task id with no task behind it is taken to be a resume still
// starting that task. Older than that, the resume is taken to have died before creating it and Sweep gives the
// tab back; a resume slower than this finds it so (Store.Holds says).
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
	// A reservation whose submit died would hold its tab for ever: fail it so the tab's task counts as over.
	if err := (&aitask.Store{DB: store.DB, Profile: store.Profile}).ReapReservations(); err != nil {
		return res, err
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
			if err := parkUnstartedResume(ctx, store, t); err != nil {
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

// parkUnstartedResume gives the person a tab a resume held under the id of a task it never created: the resume
// writes the hold before it starts the task, and died in between. One statement, holding only while the tab still
// is under that id, the hold is older than ResumeHoldTTL and no task has that id; a task created since is the
// resume's own and is judged like any other.
func parkUnstartedResume(ctx context.Context, store Store, t Tab) error {
	if t.State != StateActive || t.TaskID == "" {
		return nil
	}
	cutoff := time.Now().UTC().Add(-ResumeHoldTTL).Format(time.RFC3339)
	_, err := store.DB.ExecContext(ctx,
		`UPDATE application_tabs SET state = ?, task_id = '', updated_at = ?
		 WHERE application_id = ? AND state = ? AND task_id = ? AND updated_at < ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)
		   AND NOT EXISTS (SELECT 1 FROM ai_tasks k WHERE k.id = application_tabs.task_id)`,
		string(StateParked), now(), t.ApplicationID, string(StateActive), t.TaskID, cutoff, store.Profile)
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
