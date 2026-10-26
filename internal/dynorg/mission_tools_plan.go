package dynorg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/monomind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The lead's planning tools (plan section 8.1). They wrap the tasks package's
// session operations, as the session-bound agent of the conversation.
const (
	ToolMissionPropose  = "mission_propose"
	ToolMissionPlan     = "mission_plan"
	ToolMissionAddTasks = "mission_add_tasks"
	ToolMissionSplit    = "mission_split"
	ToolMissionStatus   = "mission_status"
	ToolMissionReplan   = "mission_replan"
)

// IsMissionPlanTool says whether name is one of the tools above.
func IsMissionPlanTool(name string) bool {
	switch name {
	case ToolMissionPropose, ToolMissionPlan, ToolMissionAddTasks, ToolMissionSplit, ToolMissionStatus, ToolMissionReplan:
		return true
	}
	return false
}

func planTaskSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Tasks of the plan.", "items": obj(map[string]any{
		"ref":        str("Name of the task inside this call (letters, digits, . - _), used by depends_on and parent."),
		"title":      str("One outcome, in a line."),
		"notes":      str("Context the worker needs."),
		"acceptance": str("How anyone checks it is done: a command in backticks, a named test, a file that must exist, an exact output, or \"rubric: ...\" for a reviewer. Required (not for epics: say what the children deliver)."),
		"kind":       str("task (default), epic (groups children, never worked itself) or check (a verifier's task)."),
		"parent":     str("Ref of an epic of this call, or #<id> of one already in the session."),
		"depends_on": strList("Refs of this call, or #<id> of tasks in the session, that must be done first."),
		"files":      strList("Relative paths of the files the task touches (required for task and check)."),
		"size":       str("xs, s or m (required; l is refused: split it)."),
		"risk":       str("Optional: low, medium or high."),
		"executor":   str("Optional suggested executor: lead, worker or subagent."),
	}, "ref", "title", "acceptance")}
}

func splitsSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Tasks to split (only a task that has not begun).", "items": obj(map[string]any{
		"task_id": map[string]any{"type": "integer", "description": "The task to replace."},
		"parts": map[string]any{"type": "array", "description": "Two or more parts; they keep the task's files, size and dependencies.", "items": obj(map[string]any{
			"title": str("The part's outcome."), "acceptance": str("How the part is checked."),
			"notes": str("Optional notes of the part; the task's by default."),
			"files": strList("Optional files the part touches; the task's by default."),
			"size":  str("Optional size xs, s or m; the task's by default."),
		}, "title", "acceptance")},
	}, "task_id", "parts")}
}

// MissionPlanToolSpecs are the lead's mission tools for this conversation:
// none without coding access, mission_propose before the user confirmed, and
// the planning tools after.
func MissionPlanToolSpecs(m *MissionConfig) []monomind.ToolSpec {
	if !m.Enabled() {
		return nil
	}
	specs := []monomind.ToolSpec{{Name: ToolMissionPropose,
		Description: "Ask the user to confirm a mission: use it when the goal is large (many steps, many files, hours of work) and would be lost in one chat. Shows the user a card with one click to confirm; do not plan until they do.",
		Schema:      obj(map[string]any{"title": str("Short name of the mission."), "goal": str("The goal in a few sentences."), "reason": str("Why a planned mission fits.")}, "title", "goal")}}
	if !m.Planning() {
		return specs
	}
	return append(specs,
		monomind.ToolSpec{Name: ToolMissionPlan,
			Description: "Write the draft plan of the mission as a session on the task board: title, goal and every task. Once per conversation. Tasks are validated; a rejected plan writes nothing and lists every reason per task. Returns the review brief and the reviewer model.",
			Schema:      obj(map[string]any{"title": str("Short name of the mission."), "goal": str("Goal, success criteria and non-goals."), "tasks": planTaskSchema()}, "title", "goal", "tasks")},
		monomind.ToolSpec{Name: ToolMissionAddTasks,
			Description: "Add tasks to the mission's session. Validated like mission_plan. In a draft they wait for approval; after approval they are Ready at once.",
			Schema:      obj(map[string]any{"tasks": planTaskSchema()}, "tasks")},
		monomind.ToolSpec{Name: ToolMissionSplit,
			Description: "Replace one task that has not begun by two or more smaller ones.",
			Schema: obj(map[string]any{"task_id": map[string]any{"type": "integer", "description": "The task to split."},
				"parts": splitsSchema()["items"].(map[string]any)["properties"].(map[string]any)["parts"]}, "task_id", "parts")},
		monomind.ToolSpec{Name: ToolMissionStatus,
			Description: "Read the mission as it stands: the session, its state, every task with its state and dependencies, recent events, and the review rounds left. It is rebuilt from the board, so use it after a restart or a long gap. Takes no arguments.",
			Schema:      obj(map[string]any{})},
		monomind.ToolSpec{Name: ToolMissionReplan,
			Description: "Revise the draft after the plan review: add tasks and split tasks in one call, with a note on why. At most 2 review rounds; after that the plan stands for the operator.",
			Schema:      obj(map[string]any{"note": str("Why the plan changes (the reviewer's points you accepted)."), "add": planTaskSchema(), "split": splitsSchema()}, "note")},
	)
}

// handleMission runs one of the lead's mission planning tools.
func (c *Conductor) handleMission(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if name == ToolMissionPropose {
		return c.missionPropose(args)
	}
	m, err := c.mission()
	if err != nil {
		return "", err
	}
	switch name {
	case ToolMissionPlan:
		return c.missionPlan(ctx, m, args)
	case ToolMissionAddTasks:
		var a struct {
			Tasks []PlanTask `json:"tasks"`
		}
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		se, err := m.mustActive(ctx)
		if err != nil {
			return "", err
		}
		ids, err := m.writeTasks(ctx, se, a.Tasks)
		if err != nil {
			return "", err
		}
		return c.missionResult(ctx, m, se.ID, map[string]any{"task_ids": ids})
	case ToolMissionSplit:
		var a SplitSpec
		if err := json.Unmarshal(orEmpty(args), &a); err != nil {
			return "", fmt.Errorf("bad arguments: %v", err)
		}
		se, err := m.mustActive(ctx)
		if err != nil {
			return "", err
		}
		made, err := m.split(ctx, se, a)
		if err != nil {
			return "", err
		}
		return c.missionResult(ctx, m, se.ID, map[string]any{"parts": taskViews(made)})
	case ToolMissionStatus:
		return c.missionStatus(ctx, m)
	case ToolMissionReplan:
		return c.missionReplan(ctx, m, args)
	}
	return "", fmt.Errorf("unknown org tool %q", name)
}

func (c *Conductor) missionPropose(args json.RawMessage) (string, error) {
	m := c.cfg.Mission
	if !m.Enabled() {
		return "", errors.New("missions need a coder chat with coding, workspace or full access")
	}
	if m.Confirmed {
		return "", errors.New("the user already confirmed a mission: plan it with mission_plan")
	}
	var a chatevents.MissionProposePayload
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Goal) == "" {
		return "", errors.New("a proposal needs a title and a goal")
	}
	c.cfg.Emit.Emit(chatevents.EventMissionPropose, a)
	return marshal(map[string]any{"proposed": true, "next": "The user sees a card with one click to confirm. Do not plan or start work until they confirm; carry on with what the user asked, or wait."})
}

func (c *Conductor) missionPlan(ctx context.Context, m *MissionConfig, args json.RawMessage) (string, error) {
	var a struct {
		Title string     `json:"title"`
		Goal  string     `json:"goal"`
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	key, err := m.planKey(ctx, a)
	if err != nil {
		return "", err
	}
	if open, err := m.active(ctx); err != nil {
		return "", err
	} else if open != nil && open.ClientID == key {
		// A retried mission_plan of this round: the same draft, not an error.
		return c.missionResult(ctx, m, open.ID, map[string]any{"session_id": open.ID, "deduped": true})
	} else if open != nil {
		return "", fmt.Errorf("this conversation already has an open mission session #%d (%s): continue it with mission_status, mission_add_tasks or mission_replan; a new one starts when the operator stops or finishes it", open.ID, open.State)
	}
	// Validate before the session exists, so a rejected plan leaves nothing.
	if rej := ValidatePlan(a.Tasks, map[int64]bool{}, 0, MaxPlanTasks); len(rej) > 0 {
		return "", &RejectedError{rej}
	}
	se, err := m.Store.CreateSession(ctx, m.ProfileID, tasks.SessionInput{Title: a.Title, Goal: a.Goal, ConversationID: m.ConversationID, ClientID: key}, m.actor(0))
	if err != nil {
		return "", err
	}
	ids, err := m.writeTasks(ctx, se, a.Tasks)
	if err != nil {
		return "", fmt.Errorf("session #%d was created: %w", se.ID, err)
	}
	return c.missionResult(ctx, m, se.ID, map[string]any{"session_id": se.ID, "task_ids": ids})
}

func (c *Conductor) missionReplan(ctx context.Context, m *MissionConfig, args json.RawMessage) (string, error) {
	var a struct {
		Note  string      `json:"note"`
		Add   []PlanTask  `json:"add"`
		Split []SplitSpec `json:"split"`
	}
	if err := json.Unmarshal(orEmpty(args), &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if strings.TrimSpace(a.Note) == "" {
		return "", errors.New("say why the plan changes (note)")
	}
	if len(a.Add) == 0 && len(a.Split) == 0 {
		return "", errors.New("nothing to change: pass add or split")
	}
	se, err := m.mustActive(ctx)
	if err != nil {
		return "", err
	}
	draft := se.State == tasks.SessionDraft
	if draft && m.round(ctx, se.ID) >= MaxReviewRounds {
		return "", fmt.Errorf("the plan has had its %d review rounds: it stands as it is. Tell the user it is ready for approval", MaxReviewRounds)
	}
	for _, s := range a.Split {
		if rej := validateSplit(s); len(rej) > 0 {
			return "", &RejectedError{rej}
		}
	}
	out := map[string]any{}
	if len(a.Add) > 0 {
		if out["task_ids"], err = m.writeTasks(ctx, se, a.Add); err != nil {
			return "", err
		}
	}
	for _, s := range a.Split {
		if _, err := m.split(ctx, se, s); err != nil {
			return "", fmt.Errorf("split of #%d: %w (the earlier changes of this call were written)", s.TaskID, err)
		}
	}
	if draft {
		if err := m.addRound(ctx, se.ID, a.Note); err != nil {
			return "", err
		}
	}
	return c.missionResult(ctx, m, se.ID, out)
}

// missionResult emits the plan card for the session and builds a tool result
// from extra plus the state of the plan and, in a draft with rounds left, the
// brief for the reviewer.
func (c *Conductor) missionResult(ctx context.Context, m *MissionConfig, id int64, extra map[string]any) (string, error) {
	se, err := m.Store.GetSession(ctx, m.ProfileID, id, m.actor(id))
	if err != nil {
		return "", err
	}
	card, err := c.planCard(ctx, m, se)
	if err != nil {
		return "", err
	}
	extra["state"] = se.State
	extra["task_count"] = card.TaskCount
	extra["estimated_budget_usd"] = card.EstimatedBudgetUSD
	c.addParallel(ctx, m, id, extra)
	if se.State != tasks.SessionDraft {
		return marshal(extra)
	}
	left := MaxReviewRounds - card.Round
	extra["review_rounds_left"] = left
	if left == 0 {
		extra["next"] = "The plan stands. Tell the user it is ready for the operator's approval, then stop; do not execute anything."
		return marshal(extra)
	}
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, id, m.actor(id))
	if err != nil {
		return "", err
	}
	rev := ReviewerFor(c.cfg.Staffer)
	extra["review"] = map[string]any{"runtime": rev.Runtime, "model": rev.Model, "brief": BuildReviewBrief(plan.Session, plan.Tasks)}
	extra["next"] = "Start a read-only reviewer worker (org_spawn, role reviewer) on review.brief with that runtime and model, read its report, then apply the right points with mission_replan. Do not execute any task: the session waits for the operator's approval."
	return marshal(extra)
}

type taskView struct {
	ID         int64    `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Kind       string   `json:"kind,omitempty"`
	ParentID   int64    `json:"parent_id,omitempty"`
	DependsOn  []int64  `json:"depends_on,omitempty"`
	Acceptance string   `json:"acceptance"`
	Assignee   string   `json:"assignee,omitempty"`
	Blocked    bool     `json:"blocked,omitempty"`
	Verified   bool     `json:"verified,omitempty"`
	Files      []string `json:"files,omitempty"`
	Size       string   `json:"size,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	Retries    int      `json:"retries,omitempty"`
	// FailureNotes are what verification said about a task it sent back or blocked.
	FailureNotes []string `json:"failure_notes,omitempty"`
}

func taskViews(ts []tasks.Task) []taskView {
	out := make([]taskView, 0, len(ts))
	for _, t := range ts {
		v := taskView{ID: t.ID, Title: t.Title, Status: string(t.Status), Kind: t.Kind, ParentID: t.ParentID, DependsOn: t.DependsOn,
			Acceptance: t.Acceptance, Assignee: t.Assignee, Blocked: t.Blocked, Verified: t.Verified}
		if meta, ok := ParseTaskMeta(t.Notes); ok {
			v.Files, v.Size, v.Risk = meta.Files, meta.Size, meta.Risk
		}
		out = append(out, v)
	}
	return out
}

// missionStatus rebuilds the mission from the board alone.
func (c *Conductor) missionStatus(ctx context.Context, m *MissionConfig) (string, error) {
	se, err := m.latest(ctx)
	if err != nil {
		return "", err
	}
	if se == nil {
		return marshal(map[string]any{"session": nil, "next": "No mission session yet: understand the goal, then mission_plan."})
	}
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil {
		return "", err
	}
	events, err := m.Store.SessionEvents(ctx, m.ProfileID, se.ID, 20, m.actor(se.ID))
	if err != nil {
		return "", err
	}
	views := taskViews(plan.Tasks)
	for i, t := range plan.Tasks {
		views[i].Retries = t.Retries
		if t.Retries > 0 || t.Blocked {
			if d, err := m.Store.SessionTask(ctx, m.ProfileID, se.ID, t.ID, m.actor(se.ID)); err == nil {
				views[i].FailureNotes = failureNotes(d)
			}
		}
	}
	out := map[string]any{"session": plan.Session, "tasks": views, "recent_events": events}
	pv := c.parallelView(plan.Tasks)
	out["parallelism"], out["writers"] = pv.Parallelism, pv.Writers
	if pv.Hint != "" {
		out["parallel_hint"] = pv.Hint
	}
	switch plan.Session.State {
	case tasks.SessionDraft:
		out["review_rounds_left"] = max(0, MaxReviewRounds-m.round(ctx, se.ID))
		out["next"] = "Draft: waiting for the operator's approval. Do not execute any task."
	case tasks.SessionRunning:
		out["next"] = "Approved and running: work the Ready tasks in dependency order."
	case tasks.SessionPaused:
		out["next"] = "Paused by the operator: do not start new work."
	default:
		out["next"] = "The session is closed: tell the user how it ended; a new mission needs a new plan."
	}
	return marshal(out)
}
