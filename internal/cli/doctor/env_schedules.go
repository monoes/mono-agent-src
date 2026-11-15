package doctor

import (
	"context"

	"github.com/monoes/mono-agent/internal/account"
	"github.com/monoes/mono-agent/internal/health"
	"github.com/monoes/mono-agent/internal/workflow"
)

// addSchedulesPausedHook feeds the workflows.schedules_paused row. It reads the sign-in verdict already loaded and
// the stored workflows: no refresh, no network, nothing written, and the sign-in is not consumed.
func addSchedulesPausedHook(env *health.Env, open func() (*workflow.HybridWorkflowStore, error)) {
	env.SchedulesPaused = func(ctx context.Context) (health.SchedulesPausedInfo, error) {
		return schedulesPausedInfo(ctx, env, open, account.CurrentStatus())
	}
}

func schedulesPausedInfo(ctx context.Context, env *health.Env, open func() (*workflow.HybridWorkflowStore, error), st account.Status) (health.SchedulesPausedInfo, error) {
	info := health.SchedulesPausedInfo{Paused: !st.Allowed(), Reason: string(st.Reason)}
	if !info.Paused || env.DB == nil {
		return info, nil
	}
	store, err := open()
	if err != nil {
		return info, err
	}
	if store == nil { // no workflow folder: the database alone holds the workflows
		store = workflow.NewHybridWorkflowStore(nil, workflow.NewSQLiteWorkflowStore(env.DB))
	}
	wfs, err := store.ListAllProfilesWorkflows(ctx)
	if err != nil {
		return info, err
	}
	ids, err := workflow.PausedTriggerWorkflows(ctx, store, wfs, st)
	info.Workflows = len(ids)
	return info, err
}
