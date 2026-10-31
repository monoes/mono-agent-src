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

// ResumePending is the task id a resume writes into the tab before its task exists: no task matches it, so nothing
// can act on the tab while the new task is created.
const ResumePending = "resume-pending"

// ResumeHoldTTL is how long a ResumePending hold is taken to be a resume still in flight. Older than that, the
// resume died between its two state writes and Sweep gives the tab back.
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

// recoverStaleResume settles a tab a resume left under ResumePending past ResumeHoldTTL: bound to the
// application's open task when it has one, else parked for the person. Each statement holds only while the tab
// still is that stale hold, so a resume that finishes meanwhile keeps its tab.
func recoverStaleResume(ctx context.Context, store Store, t Tab) error {
	if t.State != StateActive || t.TaskID != ResumePending {
		return nil
	}
	cutoff := time.Now().UTC().Add(-ResumeHoldTTL).Format(time.RFC3339)
	const open = `SELECT k.id FROM ai_tasks k WHERE k.application_id = application_tabs.application_id AND k.profile_id = ? AND k.status NOT IN (?, ?, ?)
		 ORDER BY k.created_at DESC LIMIT 1`
	terminal := []any{aitask.StatusComplete, aitask.StatusFailed, aitask.StatusCancelled}
	hold := ` WHERE application_id = ? AND state = ? AND task_id = ? AND updated_at < ?
		   AND application_id IN (SELECT id FROM applications WHERE profile_id = ?)`
	args := append(append([]any{store.Profile}, terminal...), now())
	args = append(args, t.ApplicationID, string(StateActive), ResumePending, cutoff, store.Profile)
	if _, err := store.DB.ExecContext(ctx,
		`UPDATE application_tabs SET task_id = COALESCE((`+open+`), task_id), updated_at = ?`+hold+`
		   AND EXISTS (`+open+`)`, append(args, append([]any{store.Profile}, terminal...)...)...); err != nil {
		return err
	}
	_, err := store.DB.ExecContext(ctx,
		`UPDATE application_tabs SET state = ?, updated_at = ?`+hold,
		string(StateParked), now(), t.ApplicationID, string(StateActive), ResumePending, cutoff, store.Profile)
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
