package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// maxLeaseMinutes is the longest claim, a day, as the store's MaxLease.
const maxLeaseMinutes = 1440

// taskClaimResult is what task_claim returns: the task (null when next found
// nothing to take) and what to do next, which names tools and ids and never a
// task's text.
type taskClaimResult struct {
	Profile   tasks.Profile `json:"profile"`
	Task      *taskView     `json:"task"`
	NextSteps []string      `json:"next_steps"`
	Note      string        `json:"note"`
}

// taskAddResult is what task_add returns, as `task add --json` has it.
type taskAddResult struct {
	Profile tasks.Profile `json:"profile"`
	Created bool          `json:"created"`
	Task    taskView      `json:"task"`
	Note    string        `json:"note"`
}

// taskWriteTools change the board as an agent may (spec 5.1); they follow
// --allow-mutations like every mutating tool. None approves, edits, moves or
// archives a task: that is the operator's.
func taskWriteTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false} // none deletes anything
	return []tool{
		{
			name: "task_claim",
			description: taskBoardIntro +
				"Takes a ready task for you, or renews your hold on a task you hold: {profile, task, next_steps, note}. " +
				"Give id (a task's number, from task_list or task_next) or next: true, which takes the top of Ready, else a claim whose lease has run out, " +
				"in one step, so two agents never get the same task; with nothing to take, task is null. " +
				"lease_minutes: how long you hold it (default 30, at most 1440: more is cut to 1440). " +
				"A task_comment extends your claim to 30 minutes from the comment, if that is later, and never shortens it: it does not add lease_minutes. " +
				"To hold a long lease past its end, comment once less than 30 minutes of it are left, before it runs out, " +
				"or call task_claim again with this task's id and lease_minutes (not next: true), which extends the claim to that lease counted from now, if that is later. " +
				"This server names you (agent:<client>#<4 hex digits>), the same for every call of this session: no argument names you. " +
				"A claim belongs to this server process: after it restarts you are a new claimant, and a task you held frees itself when its lease ends. " +
				"Refusals: not_ready (the task is not in Ready, or the operator is working on it), claimed (another agent holds it, and until when), " +
				"limit (the task's history is full: if you hold the task, finish or release it; otherwise leave the task to the operator and tell the user), not_found, invalid_input.",
			schema: objSchema(map[string]interface{}{
				"id":            intParam("A ready task's number (give id or next, not both)"),
				"next":          boolParam("true: take the top of Ready (give id or next, not both)"),
				"lease_minutes": intParam("How long you hold it, in minutes (default 30, at most 1440)"),
				"session":       sessionArg(),
			}),
			annotations: write,
			mutating:    true,
			handler:     toolTaskClaim,
		},
		{
			name: "task_comment",
			description: taskBoardIntro +
				"Adds a progress note to a task you hold and extends your claim to 30 minutes from now, if that is later (it never shortens it, and it does not add the lease_minutes you claimed with): {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"id: the task's number; text: what you did or found (at most 8 KiB; longer is cut). " +
				"Refusals: not_claimant (you do not hold the task: claim it first), limit (the task has 500 events: finish or release it), not_found, invalid_input (no text).",
			schema: objSchema(map[string]interface{}{
				"id":      intParam("The number of a task you hold"),
				"text":    strParam("Your progress note"),
				"session": sessionArg(),
			}, "id", "text"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskComment,
		},
		{
			name: "task_finish",
			description: taskBoardIntro +
				"Hands a task you hold back to the operator, in Review, with a result or a question (exactly one): {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"result: what you did. question: what you need to know before you can go on; the operator answers and puts the task back in Ready. " +
				"Your claim ends either way. Refusals: not_claimant, not_found, invalid_input (neither or both).",
			schema: objSchema(map[string]interface{}{
				"id":       intParam("The number of a task you hold"),
				"result":   strParam("What you did (give result or question)"),
				"question": strParam("What you need to know before you can go on (give result or question)"),
				"session":  sessionArg(),
			}, "id"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskFinish,
		},
		{
			name: "task_release",
			description: taskBoardIntro +
				"Gives a task you hold back to Ready, behind the other ready tasks, when you cannot do it: {profile, task, note}, " +
				"the task's notes cut at 1000 characters as in task_list. " +
				"note: why (optional). Your claim ends. Refusals: not_claimant, not_found.",
			schema: objSchema(map[string]interface{}{
				"id":      intParam("The number of a task you hold"),
				"note":    strParam("Why you give it back (optional)"),
				"session": sessionArg(),
			}, "id"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskRelease,
		},
		{
			name: "task_add",
			description: taskBoardIntro +
				"Adds a task to this profile's Inbox, where the operator reads it and decides whether it is worked: {profile, created, task, note}. " +
				"You cannot add to Ready. With session it adds to that task session instead: a draft task until the operator approves the plan, Ready at once in a running session (the session's max_tasks bounds it); it needs acceptance. " +
				"title: at most 200 characters; notes: optional, at most 64 KiB (longer is cut). " +
				"Refusals: limit (the board is full: 2,000 open tasks), invalid_input (no title).",
			schema: objSchema(map[string]interface{}{
				"title":              strParam("The task, in one line"),
				"notes":              strParam("More about it (optional)"),
				"session":            sessionArg(),
				"acceptance":         strParam("With session: how anyone will know the task is done (required for a session task)"),
				"depends_on":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "integer"}, "description": "With session: numbers of session tasks that must be done first"},
				"parent":             intParam("With session: the number of the session task it belongs under"),
				"kind":               strParam("With session: task (the default), epic (a group, never handed out) or check (a verifier's task)"),
				"suggested_executor": strParam("With session: the executor it suits: lead, worker or subagent"),
			}, "title"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskAdd,
		},
	}
}

// leaseOf is task_claim's lease_minutes as the store takes it: 0 (the store's
// 30 minutes) when none or less is given, never more than a day. The minutes
// are bounded before they are multiplied, so a huge number cannot overflow
// into a short lease.
func leaseOf(minutes int64) time.Duration {
	switch {
	case minutes <= 0:
		return 0
	case minutes > maxLeaseMinutes:
		minutes = maxLeaseMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// nextSteps tells an agent how to go on with a task it has just claimed.
func nextSteps(t *tasks.Task) []string {
	if t == nil {
		return []string{"Nothing is ready to claim. Do not invent work: tell the user, or call task_list to see what is in progress or waiting for review."}
	}
	until := ""
	if t.Claim != nil {
		until = t.Claim.Until.UTC().Format(time.RFC3339)
	}
	return []string{
		fmt.Sprintf("Work task %d. Report progress with task_comment {\"id\": %d, \"text\": \"...\"}: each comment extends your claim to 30 minutes from the comment, if that is later (it never shortens it).", t.ID, t.ID),
		fmt.Sprintf("When it is done, task_finish {\"id\": %d, \"result\": \"what you did\"}; to ask the user something first, task_finish {\"id\": %d, \"question\": \"what you need to know\"}. Both send it to Review.", t.ID, t.ID),
		fmt.Sprintf("If you cannot do it, task_release {\"id\": %d, \"note\": \"why\"} puts it back in Ready.", t.ID),
		fmt.Sprintf("Your claim ends at %s unless you renew it; after that another agent may take the task over.", until),
	}
}

func toolTaskClaim(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID           taskIDArg `json:"id"`
		Next         bool      `json:"next"`
		LeaseMinutes numberArg `json:"lease_minutes"`
		Session      taskIDArg `json:"session"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	if (a.ID > 0) == a.Next {
		return nil, invalidArgs("give id (a ready task's number, from task_list or task_next) or next: true (the top of Ready), one of the two")
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	lease := leaseOf(int64(a.LeaseMinutes))
	var t *tasks.Task
	switch {
	case a.Next && a.Session > 0:
		t, err = store.SessionNext(ctx, p.ID, int64(a.Session), actor, true, lease)
	case a.Next:
		t, err = store.Next(ctx, p.ID, actor, true, lease)
	default:
		var claimed tasks.Task
		if claimed, err = claimTask(ctx, store, p.ID, int64(a.Session), int64(a.ID), actor, lease); err == nil {
			t = &claimed
		}
	}
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskClaimResult{Profile: p, Task: viewPtr(t), NextSteps: nextSteps(t), Note: untrustedNote}, nil
}

func toolTaskComment(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID      taskIDArg `json:"id"`
		Text    string    `json:"text"`
		Session taskIDArg `json:"session"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := commentTask(ctx, store, p.ID, int64(a.Session), id, a.Text, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskFinish(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID       taskIDArg `json:"id"`
		Result   string    `json:"result"`
		Question string    `json:"question"`
		Session  taskIDArg `json:"session"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := finishTask(ctx, store, p.ID, int64(a.Session), id, tasks.Outcome{Result: a.Result, Question: a.Question}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskRelease(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID      taskIDArg `json:"id"`
		Note    string    `json:"note"`
		Session taskIDArg `json:"session"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := releaseTask(ctx, store, p.ID, int64(a.Session), id, a.Note, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskAdd(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Title             string      `json:"title"`
		Notes             string      `json:"notes"`
		Session           taskIDArg   `json:"session"`
		Acceptance        string      `json:"acceptance"`
		DependsOn         []taskIDArg `json:"depends_on"`
		Parent            taskIDArg   `json:"parent"`
		Kind              string      `json:"kind"`
		SuggestedExecutor string      `json:"suggested_executor"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	if a.Session > 0 {
		deps := make([]int64, 0, len(a.DependsOn))
		for _, d := range a.DependsOn {
			deps = append(deps, int64(d))
		}
		t, err := store.AddSessionTask(ctx, p.ID, int64(a.Session), tasks.SessionTaskInput{
			Title: a.Title, Notes: a.Notes, Acceptance: a.Acceptance, Kind: a.Kind, ParentID: int64(a.Parent),
			DependsOn: deps, SuggestedExecutor: a.SuggestedExecutor,
		}, actor)
		if err != nil {
			return nil, taskToolErr(err)
		}
		return taskAddResult{Profile: p, Created: true, Task: viewOf(t), Note: untrustedNote}, nil
	}
	if a.Acceptance != "" || len(a.DependsOn) > 0 || a.Parent > 0 || a.Kind != "" || a.SuggestedExecutor != "" {
		return nil, invalidArgs("acceptance, depends_on, parent, kind and suggested_executor belong to a session task: give session")
	}
	t, created, err := store.Add(ctx, p.ID, tasks.AddInput{Title: a.Title, Notes: a.Notes}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskAddResult{Profile: p, Created: created, Task: viewOf(t), Note: untrustedNote}, nil
}
