package dynorg

import (
	"context"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The start rules every route to a worker shares. mission_next, mission_assign and
// org_spawn{task_id} all end in missionPrepare, which asks startRefusal: the budget stop,
// the failure cap, the writer file rule, and two hard bounds on how often one task is
// started, so a steered lead cannot loop org_spawn on a failing task (a retry of a task is
// outside the worker cap, and an unlimited budget bounds nothing).

const (
	// defaultSpawnsPerTask is how many workers one task may get in a Conductor's turn.
	defaultSpawnsPerTask = 6
	// maxStartsPerTask is how many workers one task may get in all, across turns. It is
	// counted from the task's stored "assigned" events, so a restart does not reset it.
	maxStartsPerTask = 12
)

func (m *MissionRun) spawnsPerTask() int {
	if m.MaxSpawnsPerTask > 0 {
		return m.MaxSpawnsPerTask
	}
	return defaultSpawnsPerTask
}

// storedStarts counts the workers a task has been given, from its history: missionStarted
// records each as an "assigned" event naming a worker or subagent (the lead's own is not one).
func storedStarts(events []tasks.Event) int {
	n := 0
	for _, e := range events {
		if e.Kind == "assigned" && (strings.HasPrefix(e.Note, ExecWorker+":") || strings.HasPrefix(e.Note, ExecSubagent+":")) {
			n++
		}
	}
	return n
}

// startRefusal is why a task may not be started now ("" = it may), for a start that is not
// the scheduler's own pass.
func (c *Conductor) startRefusal(ctx context.Context, mc *MissionRun, se tasks.Session, t tasks.Task, events []tasks.Event, exec, access string) string {
	if note := c.missionBudget(ctx, se); note != "" {
		return note
	}
	if why := c.failureCap(mc, t); why != "" {
		return why
	}
	writers, err := c.activeWriters(ctx, mc, se.ID)
	if err != nil {
		return "the running writers could not be read: " + err.Error()
	}
	if why := writerOverlap(t, exec, access, writers); why != "" {
		return why
	}
	if exec == ExecLead {
		return ""
	}
	c.msched.mu.Lock()
	c.msched.init()
	turn := c.msched.starts[t.ID]
	c.msched.mu.Unlock()
	if turn >= mc.spawnsPerTask() {
		return fmt.Sprintf("it was started %d times this turn already: read its comments, then replan it or do it yourself", turn)
	}
	if n := storedStarts(events); n >= maxStartsPerTask {
		return fmt.Sprintf("it was started %d times in all (the limit): the operator decides what happens to it", n)
	}
	return ""
}

func (c *Conductor) failureCap(mc *MissionRun, t tasks.Task) string {
	c.msched.mu.Lock()
	failed := c.msched.failures[t.ID]
	c.msched.mu.Unlock()
	if failed >= mc.attempts() {
		return fmt.Sprintf("its worker failed %d times this turn: read its comments, then replan it or do it yourself", failed)
	}
	return ""
}

func writerOverlap(t tasks.Task, exec, access string, writers []writerClaim) string {
	if !isWriter(exec, access) {
		return ""
	}
	mine := TaskFiles(t.Notes)
	for _, w := range writers {
		if w.task != t.ID && filesOverlap(mine, w.files) {
			return fmt.Sprintf("it touches files that writer #%d works on (or declares none): it waits", w.task)
		}
	}
	return ""
}
