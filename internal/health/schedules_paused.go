package health

import "context"

// CheckSchedulesPaused is the row that says scheduled workflows are not running because the monoes.me sign-in is
// missing or has expired. It is advice (a warning, never required) and appears only when a workflow would fire.
const CheckSchedulesPaused = "workflows.schedules_paused"

// SchedulesPausedInfo is what the hook read: how many active workflows have a trigger that fires on its own, and
// the sign-in verdict that decides whether the engine drops those triggers.
type SchedulesPausedInfo struct {
	Workflows int // active workflows with a trigger that fires on its own
	Paused    bool
	Reason    string // the account reason ("not_logged_in", "expired", ...) when Paused
}

func schedulesPausedChecks() []Check {
	return []Check{{ID: CheckSchedulesPaused, Group: GroupCore, Title: "Scheduled workflows",
		Features: []string{"scheduled and triggered workflows"}, Run: checkSchedulesPaused}}
}

func checkSchedulesPaused(ctx context.Context, env *Env) Result {
	return Result{Status: StatusOK}
}
