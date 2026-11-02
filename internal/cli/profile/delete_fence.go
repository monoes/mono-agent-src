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
