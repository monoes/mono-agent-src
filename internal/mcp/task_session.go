package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
	"github.com/monoes/mono-agent/internal/taskview"
)

// Task sessions through the task tools (docs/mastermind/plans/2026-10-25-coder-missions-task-sessions.md).
// A session is one goal with a board of its own: its tasks are worked inside it and never show on
// the personal board. The operator opens a session (approves its plan); these tools never do, and they
// never set a budget, a cap, an access level or auto-close. A server may be bound to one session with
// MONOAGENT_TASK_SESSION (what a worker's token will carry): it then acts in that session only.

// boundSessionEnv names the session an agent's process is bound to.
const boundSessionEnv = "MONOAGENT_TASK_SESSION"

// taskBindingNumber reads a process binding. An invalid nonempty value is a
// binding to no existing row (-1), never an unbound process (0).
func taskBindingNumber(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil || n <= 0 {
		return -1
	}
	return n
}

// boundSession reads boundSessionEnv. A task without its session is a damaged
// binding (-1), not an unbound Personal-board caller.
func boundSession() int64 {
	session := taskBindingNumber(os.Getenv(boundSessionEnv))
	if session == 0 && boundTask() != 0 {
		return -1
	}
	return session
}

// boundTaskEnv narrows boundSessionEnv to one task (a mission worker's).
const boundTaskEnv = "MONOAGENT_TASK_ID"

// boundTask reads boundTaskEnv: 0 only when no binding was supplied.
func boundTask() int64 { return taskBindingNumber(os.Getenv(boundTaskEnv)) }

// boundRoleEnv narrows the task binding to a verifier's ("verifier"): verify, comment and attach
// evidence to the one task, nothing else.
const boundRoleEnv = "MONOAGENT_TASK_ROLE"

func boundVerifier() bool {
	return boundTask() != 0 && strings.TrimSpace(os.Getenv(boundRoleEnv)) == "verifier"
}

// sessionIntro is added to the description of the tools that take a session.
const sessionIntro = "A task session is one goal with a board of its own: the operator approves its plan, and until then its tasks " +
	"are a draft that nobody can take. Inside a running session you work only that session's tasks. "

// sessionArg is the optional "session" argument of the tools that work a board.
func sessionArg() map[string]interface{} {
	return intParam("A task session's number (from task_session_list): work that session's board instead of the personal one; omit it for the personal board (a task of a session given by id is worked in its session anyway)")
}

// taskSessionReadTools only read sessions: they are in the default server, the read-only one included.
func taskSessionReadTools() []tool {
	ro := map[string]bool{"readOnlyHint": true, "idempotentHint": true}
	return []tool{
		{
			name: "task_session_list",
			description: taskBoardIntro + sessionIntro +
				"Lists the task sessions of the call's profile, newest first: {profile, sessions, note}, each session " +
				"{id, title_untrusted, goal_untrusted, state (draft, running, paused, done, stopped, failed), access_level, budget_usd, spent_usd, " +
				"max_tasks, max_depth, max_retries, auto_close_verified, created_by, approved_by, approved_at, archived, rev, counts {inbox, ready, in_progress, review, done, blocked, verified, total}}. " +
				"state: only sessions in that state; all: true includes archived ones. " +
				"Refusals: invalid_input (an argument this tool does not take).",
			schema: objSchema(map[string]interface{}{
				"state": strParam("Only sessions in this state: draft, running, paused, done, stopped or failed"),
				"all":   boolParam("true: include archived sessions"),
			}),
			annotations: ro,
			handler:     toolTaskSessionList,
		},
		{
			name: "task_session_get",
			description: taskBoardIntro + sessionIntro +
				"One task session with its plan: {profile, session, tasks, events, note}. tasks are the session's tasks that are not archived, in the order the plan was written, " +
				"each as task_list describes a task and also {session_id, kind (task, epic or check), parent_id, acceptance_untrusted (how the task is judged), assignee, " +
				"suggested_executor, blocked, verified, retries, depends_on (task ids that must be done first)}; a draft's tasks are in status inbox. " +
				"events are the newest 50 of the session's history. id: the session's number. Refusals: not_found (no such session, or one this server is not bound to).",
			schema: objSchema(map[string]interface{}{
				"id": intParam("The session's number, as task_session_list shows it"),
			}, "id"),
			annotations: ro,
			handler:     toolTaskSessionGet,
		},
	}
}

// taskSessionWriteTools change a session's board as an agent may; they follow --allow-mutations.
func taskSessionWriteTools() []tool {
	write := map[string]bool{"readOnlyHint": false, "destructiveHint": false}
	return []tool{
		{
			name: "task_split",
			description: taskBoardIntro + sessionIntro +
				"Replaces a task of a session that has not begun (inbox or ready, nobody holds it, no sub-tasks) by two or more: {profile, tasks, note}. " +
				"The parts keep the task's dependencies and anything that waited for it waits for all of them; the original is archived. " +
				"id: the task's number; parts: [{title, acceptance}] (2 to 20, each with acceptance criteria). " +
				"Refusals: invalid_input, limit (the session's max tasks), not_found, not_ready (the session is closed).",
			schema: objSchema(map[string]interface{}{
				"id": intParam("The number of the session task to split"),
				"parts": map[string]interface{}{"type": "array", "description": "The tasks it becomes, each with a title and acceptance criteria",
					"items": objSchema(map[string]interface{}{
						"title":      strParam("The part, in one line"),
						"acceptance": strParam("How anyone will know the part is done"),
					}, "title", "acceptance")},
			}, "id", "parts"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskSplit,
		},
		{
			name: "task_verify",
			description: taskBoardIntro + sessionIntro +
				"A verdict on a session task that is in Review: {profile, task, note}. pass: true marks it verified and nothing more: closing it to Done is the operator's " +
				"(or yours only when the operator turned on auto_close_verified for the session). pass: false sends it back to Ready with your note, up to the session's max_retries failures, " +
				"then the task is blocked and waits for the operator. You cannot verify a task you did yourself, nor one that holds a question. " +
				"id: the task's number; note: what you checked or what is wrong (required when pass is false). " +
				"Refusals: invalid_input, not_found, not_ready.",
			schema: objSchema(map[string]interface{}{
				"id":   intParam("The number of the session task in Review"),
				"pass": boolParam("true: it meets its acceptance criteria; false: it does not"),
				"note": strParam("What you checked, or what is wrong (required for a fail)"),
			}, "id", "pass"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskVerify,
		},
		{
			name: "task_artifact",
			description: taskBoardIntro + sessionIntro +
				"Attaches evidence to a session task you hold, or one in Review: {profile, artifact, note}. " +
				"id: the task's number; kind: commit, file, report, test, diff, log, url or other; ref: a commit hash, a path or a URL; note: what it shows. " +
				"Refusals: not_claimant (you do not hold it and it is not in Review), invalid_input, limit, not_found.",
			schema: objSchema(map[string]interface{}{
				"id":   intParam("The number of the session task"),
				"kind": strParam("commit, file, report, test, diff, log, url or other"),
				"ref":  strParam("A commit hash, a path, a URL"),
				"note": strParam("What it shows (optional)"),
			}, "id", "kind", "ref"),
			annotations: write,
			mutating:    true,
			handler:     toolTaskArtifact,
		},
	}
}

type taskSessionListResult struct {
	Profile  tasks.Profile          `json:"profile"`
	Sessions []taskview.SessionView `json:"sessions"`
	Note     string                 `json:"note"`
}

type taskSessionGetResult struct {
	Profile tasks.Profile               `json:"profile"`
	Session taskview.SessionView        `json:"session"`
	Tasks   []taskview.View             `json:"tasks"`
	Events  []taskview.SessionEventView `json:"events"`
	Note    string                      `json:"note"`
}

type taskSplitResult struct {
	Profile tasks.Profile   `json:"profile"`
	Tasks   []taskview.View `json:"tasks"`
	Note    string          `json:"note"`
}

type taskArtifactResult struct {
	Profile  tasks.Profile         `json:"profile"`
	Artifact taskview.ArtifactView `json:"artifact"`
	Note     string                `json:"note"`
}

func toolTaskSessionList(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		State string `json:"state"`
		All   bool   `json:"all"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	ss, err := store.ListSessions(ctx, p.ID, tasks.SessionFilter{State: tasks.SessionState(a.State), IncludeArchived: a.All}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	out := make([]taskview.SessionView, 0, len(ss))
	for _, se := range ss {
		out = append(out, taskview.SessionOf(se))
	}
	return taskSessionListResult{Profile: p, Sessions: out, Note: untrustedNote}, nil
}

// sessionEventsShown is how many events of a session's history task_session_get returns.
const sessionEventsShown = 50

func toolTaskSessionGet(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
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
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	plan, err := store.PlanOfSession(ctx, p.ID, id, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	events, err := store.SessionEvents(ctx, p.ID, id, sessionEventsShown, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskSessionGetResult{Profile: p, Session: taskview.SessionOf(plan.Session), Tasks: listViews(plan.Tasks),
		Events: taskview.SessionEventViews(events), Note: untrustedNote}, nil
}

// splitPart is one part of task_split's parts.
type splitPart struct {
	Title      string `json:"title"`
	Acceptance string `json:"acceptance"`
}

func toolTaskSplit(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID    taskIDArg   `json:"id"`
		Parts []splitPart `json:"parts"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	parts := make([]tasks.SplitPart, 0, len(a.Parts))
	for _, p := range a.Parts {
		parts = append(parts, tasks.SplitPart{Title: p.Title, Acceptance: p.Acceptance})
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	session, err := store.SessionOfTask(ctx, p.ID, id)
	if err != nil {
		return nil, taskToolErr(err)
	}
	if session == 0 {
		return nil, invalidArgs("task %d is on the personal board, not in a session", id)
	}
	ts, err := store.SplitTask(ctx, p.ID, session, id, parts, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskSplitResult{Profile: p, Tasks: listViews(ts), Note: untrustedNote}, nil
}

func toolTaskVerify(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   taskIDArg `json:"id"`
		Pass *bool     `json:"pass"`
		Note string    `json:"note"`
	}
	if err := decodeTaskArgs(args, &a); err != nil {
		return nil, err
	}
	id, err := a.ID.need()
	if err != nil {
		return nil, err
	}
	if a.Pass == nil {
		return nil, invalidArgs("pass is required: true when the task meets its acceptance criteria, false when it does not")
	}
	store, p, actor, err := s.taskBoard(ctx)
	if err != nil {
		return nil, err
	}
	session, err := store.SessionOfTask(ctx, p.ID, id)
	if err != nil {
		return nil, taskToolErr(err)
	}
	if session == 0 {
		return nil, invalidArgs("task %d is on the personal board, not in a session", id)
	}
	t, err := store.VerifySessionTask(ctx, p.ID, session, id, *a.Pass, a.Note, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	v := listView(t)
	return taskResult{Profile: p, Task: &v, Note: untrustedNote}, nil
}

func toolTaskArtifact(ctx context.Context, s *Server, args json.RawMessage) (interface{}, error) {
	var a struct {
		ID   taskIDArg `json:"id"`
		Kind string    `json:"kind"`
		Ref  string    `json:"ref"`
		Note string    `json:"note"`
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
	session, err := store.SessionOfTask(ctx, p.ID, id)
	if err != nil {
		return nil, taskToolErr(err)
	}
	if session == 0 {
		return nil, invalidArgs("task %d is on the personal board, not in a session", id)
	}
	art, err := store.AddArtifact(ctx, p.ID, session, id, tasks.ArtifactInput{Kind: a.Kind, Ref: a.Ref, Note: a.Note}, actor)
	if err != nil {
		return nil, taskToolErr(err)
	}
	return taskArtifactResult{Profile: p, Artifact: taskview.ArtifactOf(art), Note: untrustedNote}, nil
}

// The helpers below are what the tools that work a board call: with a session they work
// that session's board (a task of another board is not found), without one a task named by its
// number is worked on the board it belongs to.

func claimTask(ctx context.Context, st *tasks.Store, profile string, session, id int64, actor tasks.Actor, lease time.Duration) (tasks.Task, error) {
	if session > 0 {
		return st.SessionClaim(ctx, profile, session, id, actor, lease)
	}
	return st.ClaimAny(ctx, profile, id, actor, lease)
}

func commentTask(ctx context.Context, st *tasks.Store, profile string, session, id int64, text string, actor tasks.Actor) (tasks.Task, error) {
	if session > 0 {
		return st.SessionComment(ctx, profile, session, id, text, actor)
	}
	return st.CommentAny(ctx, profile, id, text, actor)
}

func finishTask(ctx context.Context, st *tasks.Store, profile string, session, id int64, o tasks.Outcome, actor tasks.Actor) (tasks.Task, error) {
	if session > 0 {
		return st.SessionFinish(ctx, profile, session, id, o, actor)
	}
	return st.FinishAny(ctx, profile, id, o, actor)
}

func releaseTask(ctx context.Context, st *tasks.Store, profile string, session, id int64, note string, actor tasks.Actor) (tasks.Task, error) {
	if session > 0 {
		return st.SessionRelease(ctx, profile, session, id, note, actor)
	}
	return st.ReleaseAny(ctx, profile, id, note, actor)
}
