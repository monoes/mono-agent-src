package mcp

import (
	"context"
	"encoding/json"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/taskview"
)

const (
	taskListDefault = taskview.ListDefault // task_list's limit when none is given
	taskListMax     = taskview.ListMax     // and the most it returns
)

// taskReadTools only read the board: they are in the default server, the
// read-only one included (spec 8); --api-only and --grant serve other sets.
func taskReadTools() []tool {
	return []tool{
		{
			name: "task_list",
			description: taskBoardIntro +
				"Lists the tasks of the call's profile, by column and then by position: {profile, tasks, truncated, note}, each task " +
				"{id, profile_id, title_untrusted, notes_untrusted, status, position, source_kind, source_url_untrusted, source_title_untrusted, " +
				"source_app_untrusted, claim: {by, until, stale} or null, last_event: {actor, kind, at} or null, created_at, updated_at}. " +
				"Notes longer than 1000 characters are cut here; task_get has all of them. " +
				"status: a column or several (inbox, ready, in_progress, review, done, archived), as \"ready,review\" or a list; " +
				"omitted, it is ready, in_progress and review, and inbox, done and archived are listed only when named. " +
				"limit: at most this many (default 50, at most 200); truncated is true when more tasks match than are listed " +
				"(columns come in the order above, so a full Ready can hide later columns: name the ones you want in status). " +
				"Refusals: invalid_input (an unknown status, an argument this tool does not take).",
			schema: objSchema(map[string]interface{}{
				"status": strParam("Columns to list, comma-separated: inbox, ready, in_progress, review, done, archived (default: ready, in_progress and review)"),
				"limit":  intParam("At most this many tasks (default 50, at most 200); the result says truncated when more match"),
			}),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolTaskList,
		},
		{
			name: "task_get",
			description: taskBoardIntro +
				"One task of the call's profile with its history: {profile, task, events, events_omitted, note}; the task as task_list describes it, " +
				"with all of its notes, and events [{id, at, actor, kind, from_status, to_status, note_untrusted}], oldest first, " +
				"the most recent 100 at most (events_omitted counts the older ones left out; monoagentcli task show has them all) " +
				"(kinds: created, edited, moved, claimed, reclaimed, comment, question, result, released, archived, unarchived). " +
				"id: the task's number. Refusals: not_found (no such task in this profile; a task of another profile is not found either), invalid_input.",
			schema: objSchema(map[string]interface{}{
				"id": intParam("The task's number, as task_list shows it"),
			}, "id"),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolTaskGet,
		},
		{
			name: "task_next",
			description: taskBoardIntro +
				"The task task_claim with next: true would take now, without claiming it: {profile, task or null, note}. " +
				"It is the top of Ready, else a claim whose lease has run out; never an Inbox task. " +
				"Two agents that look may see the same task: only a claim gives it to you. No arguments.",
			schema:      objSchema(nil),
			annotations: map[string]bool{"readOnlyHint": true, "idempotentHint": true},
			handler:     toolTaskNext,
		},
	}
}

func toolTaskList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		Status statusArg `json:"status"`
		Limit  numberArg `json:"limit"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	limit := int(a.Limit)
	switch {
	case limit <= 0:
		limit = taskListDefault
	case limit > taskListMax:
		limit = taskListMax
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	// One more than the limit is asked for, to see whether the limit cut anything.
	ts, err := store.List(ctx, p.ID, tasks.Filter{Statuses: a.Status, Limit: limit + 1}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	truncated := len(ts) > limit
	if truncated {
		ts = ts[:limit]
	}
	return taskListResult{Profile: p, Tasks: listViews(ts), Truncated: truncated, Note: untrustedNote}, nil
}

func toolTaskGet(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID taskIDArg `json:"id"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	store, p, _, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, events, err := store.Get(ctx, p.ID, id)
	if err != nil {
		return nil, taskToolErr(err)
	}
	recent, omitted := recentEvents(events)
	return taskGetResult{Profile: p, Task: viewOf(t), Events: eventViews(recent), EventsOmitted: omitted, Note: untrustedNote}, nil
}

func toolTaskNext(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	if err := decodeTaskArgs(args, &struct{}{}); err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	t, err := store.Next(ctx, p.ID, actor, false, 0)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskResult{Profile: p, Task: viewPtr(t), Note: untrustedNote}, nil
}
