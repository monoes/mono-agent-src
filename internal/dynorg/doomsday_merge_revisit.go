package dynorg

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// MergeReconciler is what a later start uses to revisit merge members left live by a host that died.
type MergeReconciler interface {
	MergeMembers
	LiveMembers(ctx context.Context, profile string, generation int64) ([]store.Member, error)
}

// RevisitMergeMembers is the answer to the open P1 of note 13.3: a merge member that is still live stays live,
// blocking quiescence and a new run (the store enforces both), whatever the operation's repair state says. A
// later start calls this; a member is released only when gone proves its process is gone. A false answer or an
// error leaves it live: unknown is not gone. It returns the members still live.
func RevisitMergeMembers(ctx context.Context, st MergeReconciler, profile string, generation int64, gone func(store.Member) (bool, error)) ([]store.Member, error) {
	ms, err := st.LiveMembers(ctx, profile, generation)
	if err != nil {
		return nil, err
	}
	var still []store.Member
	for _, m := range ms {
		if m.Kind != "merge" {
			continue
		}
		ok, gerr := gone(m)
		if gerr != nil || !ok {
			still = append(still, m)
			continue
		}
		if _, err := st.AppendAudit(ctx, profile, generation, "merge.revisit", fmt.Sprintf("%s released: process proven gone", m.ID)); err != nil {
			still = append(still, m)
			continue
		}
		if err := st.Release(ctx, profile, generation, m.ID); err != nil {
			still = append(still, m)
		}
	}
	return still, nil
}
