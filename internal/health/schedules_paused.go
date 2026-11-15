package health

import (
	"context"
	"fmt"
)

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
	if env.SchedulesPaused == nil {
		return Result{Status: StatusSkip}
	}
	info, err := env.SchedulesPaused(ctx)
	if err != nil {
		return Result{Status: StatusWarn, Summary: "cannot tell whether scheduled workflows are paused", Detail: err.Error()}
	}
	if !info.Paused || info.Workflows == 0 {
		return Result{Status: StatusSkip} // nothing waits on a sign-in: the row stays out of the report
	}
	why := "the monoes.me sign-in is missing"
	switch info.Reason {
	case "expired":
		why = "the monoes.me sign-in has expired"
	case "refused":
		why = "monoes.me ended this sign-in"
	}
	return Result{Status: StatusWarn, FixID: FixMonoesLogin,
		Summary: fmt.Sprintf("scheduled workflows are paused: %s (%d workflow(s) would run)", why, info.Workflows),
		Detail: "sign in with `monoagentcli account login` (or the sign-in in the app) and they start again.\n" +
			"Runs missed while signed out are dropped, not made up later."}
}
