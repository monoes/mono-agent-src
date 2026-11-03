package profile

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// afterDeleteCheck runs in deleteProfile right after its checks and before its first write. It is a test seam: a
// test commits a competing Reserve there to show that the deferred transaction fails closed.
var afterDeleteCheck = func() {}

// refuseLiveReservations refuses deleting a profile while a worker holds a live reservation on one of its task
// sessions: that worker is still running and spending against money the delete would remove. A row whose lease
// lapsed past the grace is presumed dead, and an expired row has no owner left, so neither blocks (the profile
// goes with its money). The check runs inside deleteProfile's deferred transaction: a Reserve that commits after
// it makes that transaction's first write fail (SQLITE_BUSY_SNAPSHOT), so a delete never goes ahead over a
// reservation it did not see.
func refuseLiveReservations(ctx context.Context, q scopeQuerier, profileID string) error {
	live, err := tasks.NewStore(nil).LiveReservations(ctx, q, profileID)
	if err != nil {
		return fmt.Errorf("check live worker reservations: %w", err)
	}
	if len(live) == 0 {
		return nil
	}
	var named []string
	for i, r := range live {
		if i == 5 {
			named = append(named, fmt.Sprintf("and %d more", len(live)-5))
			break
		}
		named = append(named, fmt.Sprintf("worker %q on session #%d", r.WorkerRef, r.SessionID))
	}
	return fmt.Errorf("the profile has %d live worker reservation(s) holding session budget: %s. Stop the org that runs them, or wait for their reservation lease to lapse, then delete again",
		len(live), strings.Join(named, ", "))
}

// refuseRunning refuses deleting a profile while a workflow of it is running.
func refuseRunning(ctx context.Context, q scopeQuerier, id string) error {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_executions WHERE profile_id = ? AND status IN ('RUNNING', 'QUEUED')`, id).Scan(&n); err != nil {
		return fmt.Errorf("check running workflows: %w", err)
	}
	var open int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM workflow_publications WHERE profile_id = ? OR workflow_id IN (SELECT id FROM workflows WHERE profile_id = ?)`, id, id).Scan(&open); err != nil && !strings.Contains(err.Error(), "no such table") {
		return fmt.Errorf("check unfinished workflow changes: %w", err)
	}
	if open > 0 {
		return fmt.Errorf("the profile has %d unfinished workflow change(s): run `monoagentcli doctor fix workflows.publications.recover` (or resolve them with `doctor`), then delete again", open)
	}
	if n > 0 {
		return fmt.Errorf("the profile has %d running or queued workflow execution(s): let them finish or cancel them first with `monoagentcli workflow execution cancel <execution-id>` (an execution left RUNNING by a crashed run also blocks the delete until it is cancelled)", n)
	}
	return refuseLiveReservations(ctx, q, id)
}
