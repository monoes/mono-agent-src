package dynorg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// stallNote says why mission_next started nothing, so the lead never faces an empty answer:
// who each waiting task waits for, a claim whose lease has not ended, workers and verifiers
// still running, tasks held back by a cap or a writer, or that nothing is left. "" when the
// result already explains itself (a task is only "ready to start" in a look).
func (c *Conductor) stallNote(ctx context.Context, mc *MissionRun, sid int64, res StepResult) string {
	for _, w := range res.Waiting {
		if w.Reason == "ready to start" {
			return ""
		}
	}
	plan, err := mc.Store.PlanOfSession(ctx, mc.ProfileID, sid, missionActor())
	if err != nil {
		return ""
	}
	byID := map[int64]tasks.Task{}
	for _, t := range plan.Tasks {
		byID[t.ID] = t
	}
	var parts []string
	var open int
	for _, t := range plan.Tasks {
		if t.Kind == tasks.KindEpic || tasks.DepSettled(t) || t.Status == tasks.StatusInbox {
			continue
		}
		open++
		switch {
		case t.Status == tasks.StatusReady && !t.Blocked:
			if on := unsettledDeps(t, byID); on != "" {
				parts = append(parts, fmt.Sprintf("task #%d waits for %s", t.ID, on))
			}
		case t.Status == tasks.StatusInProgress && t.Claim != nil && !t.Claim.Stale && c.taskWorker(t.ID) == nil:
			parts = append(parts, fmt.Sprintf("task #%d is claimed by %s until %s (its lease has not ended; nothing of this turn works on it)",
				t.ID, t.Claim.By, t.Claim.Until.UTC().Format(time.RFC3339)))
		}
	}
	if n := len(res.Running); n > 0 {
		var who []string
		for _, r := range res.Running {
			who = append(who, fmt.Sprintf("%s on #%d", r.roleName(), r.TaskID))
		}
		parts = append(parts, fmt.Sprintf("%d running (%s): wait for them (org_wait)", n, strings.Join(who, ", ")))
	}
	if len(res.Waiting) > 0 {
		parts = append(parts, fmt.Sprintf("%d ready task(s) did not start, see waiting for each reason", len(res.Waiting)))
	}
	if len(parts) == 0 {
		if open == 0 {
			return "nothing is left to start: every task is done or verified; call mission_finish"
		}
		return fmt.Sprintf("nothing can start: %d open task(s) are blocked or in review; read mission_status", open)
	}
	return "nothing started: " + strings.Join(parts, "; ")
}

func (r RunningTask) roleName() string {
	if r.Role == "" {
		return "worker"
	}
	return r.Role
}

// unsettledDeps lists the dependencies of t that are neither done nor verified, with status.
func unsettledDeps(t tasks.Task, byID map[int64]tasks.Task) string {
	var out []string
	for _, id := range t.DependsOn {
		d, ok := byID[id]
		if ok && tasks.DepSettled(d) {
			continue
		}
		st := "unknown"
		if ok {
			st = string(d.Status)
			if d.Blocked {
				st += ", blocked"
			}
		}
		out = append(out, fmt.Sprintf("#%d (%s)", id, st))
	}
	return strings.Join(out, ", ")
}
