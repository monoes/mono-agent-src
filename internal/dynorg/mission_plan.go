package dynorg

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/monoes/mono-agent/internal/ai/chatevents"
	"github.com/monoes/mono-agent/internal/tasks"
)

// MaxReviewRounds is how many times the lead revises a draft plan after review
// (mission_replan); after that the plan stands for the operator.
const MaxReviewRounds = 2

// MissionConfig makes a conductor's lead a mission planner (Config.Mission).
// The lead acts on the task board as one named agent, bound to the one session
// of its own conversation: it never names a session, so it cannot touch another.
type MissionConfig struct {
	Store          *tasks.Store
	ProfileID      string
	ConversationID string
	// Actor is the agent name the lead writes under.
	Actor string
	// Access is the conversation's access level; "" is full.
	Access string
	// Confirmed: the user confirmed a mission with one click (D2). Without it
	// the lead can only propose.
	Confirmed bool
}

// MissionAccess says whether an access level may run missions: coding and up.
func MissionAccess(level string) bool {
	return slices.Contains([]string{"", "coding", "workspace", "full"}, level)
}

// Enabled: the lead gets mission tools at all.
func (m *MissionConfig) Enabled() bool { return m != nil && m.Store != nil && MissionAccess(m.Access) }

// Planning: the lead gets the planning tools and prompt.
func (m *MissionConfig) Planning() bool { return m.Enabled() && m.Confirmed }

func (m *MissionConfig) actor(session int64) tasks.Actor {
	return tasks.Actor{Kind: tasks.Agent, Name: m.Actor, Session: session}
}

// round is how many review rounds the draft has had: the plan_revised events of the
// session, so a new Conductor (a restart, a second turn) keeps the count.
func (m *MissionConfig) round(ctx context.Context, session int64) int {
	n, err := m.Store.CountSessionEvents(ctx, m.ProfileID, session, tasks.EventPlanRevised, m.actor(session))
	if err != nil {
		return MaxReviewRounds // unreadable: do not hand out more rounds
	}
	return n
}

func (m *MissionConfig) addRound(ctx context.Context, session int64, note string) error {
	return m.Store.AddSessionEvent(ctx, m.ProfileID, session, tasks.EventPlanRevised, note, m.actor(session))
}

// active is the open session of this conversation (draft, running or paused),
// or nil: the store holds the link, so a new Conductor finds it again.
func (m *MissionConfig) active(ctx context.Context) (*tasks.Session, error) {
	list, err := m.Store.ListSessions(ctx, m.ProfileID, tasks.SessionFilter{}, m.actor(0))
	if err != nil {
		return nil, err
	}
	for _, se := range list { // newest first
		if se.ConversationID != m.ConversationID {
			continue
		}
		switch se.State {
		case tasks.SessionDraft, tasks.SessionRunning, tasks.SessionPaused:
			return &se, nil
		}
	}
	return nil, nil
}

// planKey is the idempotency key of this conversation's next mission_plan: the
// conversation, the plan round (how many earlier sessions of the conversation have
// been closed) and the plan itself, so a retried call (same arguments, same round)
// names the same session, while a different plan is still refused as a second open
// session and a new plan after the operator closed one gets a new key.
func (m *MissionConfig) planKey(ctx context.Context, plan any) (string, error) {
	list, err := m.Store.ListSessions(ctx, m.ProfileID, tasks.SessionFilter{IncludeArchived: true}, m.actor(0))
	if err != nil {
		return "", err
	}
	round := 0
	for _, se := range list {
		if se.ConversationID != m.ConversationID {
			continue
		}
		switch se.State {
		case tasks.SessionDraft, tasks.SessionRunning, tasks.SessionPaused:
			if !se.Archived {
				continue
			}
		}
		round++
	}
	body, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	conv, content := sha256.Sum256([]byte(m.ConversationID)), sha256.Sum256(body)
	return fmt.Sprintf("mp-%x-%d-%x", conv[:8], round, content[:8]), nil
}

// latest is the newest session of this conversation in any state, or nil.
func (m *MissionConfig) latest(ctx context.Context) (*tasks.Session, error) {
	list, err := m.Store.ListSessions(ctx, m.ProfileID, tasks.SessionFilter{IncludeArchived: true}, m.actor(0))
	if err != nil {
		return nil, err
	}
	for _, se := range list {
		if se.ConversationID == m.ConversationID {
			return &se, nil
		}
	}
	return nil, nil
}

// mustActive is the session the lead may write to.
func (m *MissionConfig) mustActive(ctx context.Context) (tasks.Session, error) {
	se, err := m.active(ctx)
	if err != nil {
		return tasks.Session{}, err
	}
	if se == nil {
		return tasks.Session{}, errors.New("this conversation has no open mission session: call mission_plan first")
	}
	return *se, nil
}

// planLimit is the most tasks the session may hold: 200 in a draft, the
// session's max_tasks once the operator approved (D5).
func planLimit(se tasks.Session) int {
	if se.State == tasks.SessionDraft {
		return min(MaxPlanTasks, se.MaxTasks)
	}
	return se.MaxTasks
}

func (c *Conductor) mission() (*MissionConfig, error) {
	m := c.cfg.Mission
	if !m.Planning() {
		return nil, errors.New("missions are not on for this conversation: the user has not confirmed one (propose it with mission_propose)")
	}
	return m, nil
}

// RejectedError is a plan the validation refused: nothing was written.
type RejectedError struct{ Rejections []Rejection }

func (e *RejectedError) Error() string {
	var b strings.Builder
	b.WriteString("plan rejected, nothing was written. Fix every task and send the plan again:")
	for _, r := range e.Rejections {
		who := "plan"
		if r.Ref != "" {
			who = "task " + r.Ref
		}
		fmt.Fprintf(&b, "\n- %s: %s", who, strings.Join(r.Reasons, "; "))
	}
	return b.String()
}

// writeTasks validates batch against the session and writes it, in an order
// that puts every task after what it names. It returns the ids by ref.
func (m *MissionConfig) writeTasks(ctx context.Context, se tasks.Session, batch []PlanTask) (map[string]int64, error) {
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil {
		return nil, err
	}
	existing := map[int64]bool{}
	for _, t := range plan.Tasks {
		existing[t.ID] = true
	}
	if rej := ValidatePlan(batch, existing, se.Counts.Stored(), planLimit(se)); len(rej) > 0 {
		return nil, &RejectedError{rej}
	}
	if rej := integrationRejections(batch, plan.Tasks); len(rej) > 0 {
		return nil, &RejectedError{rej}
	}
	ids := map[string]int64{}
	resolve := func(ref string) int64 {
		if n, ok := existingRef(ref); ok {
			return n
		}
		return ids[ref]
	}
	for _, t := range writeOrder(batch) {
		in := tasks.SessionTaskInput{
			Title: t.Title, Notes: notesWithMeta(t.Notes, TaskMeta{Files: t.Files, Size: t.Size, Risk: t.Risk}),
			Acceptance: t.Acceptance, Kind: t.Kind, SuggestedExecutor: t.Executor,
		}
		if t.Parent != "" {
			in.ParentID = resolve(t.Parent)
		}
		for _, d := range t.DependsOn {
			in.DependsOn = append(in.DependsOn, resolve(d))
		}
		made, err := m.Store.AddSessionTask(ctx, m.ProfileID, se.ID, in, m.actor(se.ID))
		if err != nil {
			return ids, fmt.Errorf("task %s was refused by the board after %d of %d were written (mission_status shows them; send the rest with mission_add_tasks): %w", t.Ref, len(ids), len(batch), err)
		}
		ids[t.Ref] = made.ID
	}
	var made []int64
	for _, id := range ids {
		made = append(made, id)
	}
	if err := m.waitForNewTasks(ctx, se, made); err != nil {
		return ids, err
	}
	return ids, nil
}

// SplitSpec is one split the lead asks for.
type SplitSpec struct {
	TaskID int64       `json:"task_id"`
	Parts  []SplitPart `json:"parts"`
}

// SplitPart is one task a task is split into.
type SplitPart struct {
	Title      string `json:"title"`
	Acceptance string `json:"acceptance"`
	// Notes, Files and Size are the part's own; a part that leaves them out keeps the
	// task's.
	Notes string   `json:"notes,omitempty"`
	Files []string `json:"files,omitempty"`
	Size  string   `json:"size,omitempty"`
}

// validateSplit checks the parts of a split against the same rules as a task
// (a title, a checkable acceptance); the children keep the files and size of
// the task they came from.
func validateSplit(s SplitSpec) []Rejection {
	var why []string
	if len(s.Parts) < 2 {
		why = append(why, "a split makes at least 2 tasks")
	}
	for i, p := range s.Parts {
		if strings.TrimSpace(p.Title) == "" {
			why = append(why, fmt.Sprintf("part %d has no title", i+1))
		}
		if !checkable(p.Acceptance) {
			why = append(why, fmt.Sprintf("part %d: acceptance is missing or not checkable", i+1))
		}
	}
	why = append(why, validateSplitMeta(s)...)
	if len(why) == 0 {
		return nil
	}
	return []Rejection{{Ref: fmt.Sprintf("#%d", s.TaskID), Reasons: why}}
}

func (m *MissionConfig) split(ctx context.Context, se tasks.Session, s SplitSpec) ([]tasks.Task, error) {
	if rej := validateSplit(s); len(rej) > 0 {
		return nil, &RejectedError{rej}
	}
	parts, err := splitParts(ctx, m, se.ID, s)
	if err != nil {
		return nil, err
	}
	return m.Store.SplitTask(ctx, m.ProfileID, se.ID, s.TaskID, parts, m.actor(se.ID))
}

// planCard emits mission.plan for the session as it stands.
func (c *Conductor) planCard(ctx context.Context, m *MissionConfig, se tasks.Session) (chatevents.MissionPlanPayload, error) {
	plan, err := m.Store.PlanOfSession(ctx, m.ProfileID, se.ID, m.actor(se.ID))
	if err != nil {
		return chatevents.MissionPlanPayload{}, err
	}
	card := chatevents.MissionPlanPayload{
		SessionID: se.ID, Title: se.Title, Goal: se.Goal, Round: m.round(ctx, se.ID),
		TaskCount: len(plan.Tasks), EstimatedBudgetUSD: estimateBudget(plan.Tasks),
	}
	c.cfg.Emit.Emit(chatevents.EventMissionPlan, card)
	return card, nil
}

// estimateBudget is the rough cost of the tasks from their size labels.
func estimateBudget(ts []tasks.Task) float64 {
	var usd float64
	for _, t := range ts {
		if meta, ok := ParseTaskMeta(t.Notes); ok {
			usd += sizeBudgetUSD[meta.Size]
		}
	}
	return usd
}

// Mission is the conductor's mission setup, nil when it has none.
func (c *Conductor) Mission() *MissionConfig { return c.cfg.Mission }

// OpenSessionID is the id of the conversation's open mission session, or 0.
// A conversation that started a mission stays confirmed after a restart.
func (m *MissionConfig) OpenSessionID(ctx context.Context) int64 {
	if se, err := m.active(ctx); err == nil && se != nil {
		return se.ID
	}
	return 0
}

// HasOpenSession says whether the conversation has a mission session that is
// still open (see OpenSessionID).
func (m *MissionConfig) HasOpenSession(ctx context.Context) bool {
	se, err := m.active(ctx)
	return err == nil && se != nil
}
