package tasks

import (
	"fmt"
	"time"
)

// SessionState is where a task session is in its life. Only a running session
// hands out tasks.
type SessionState string

const (
	SessionDraft   SessionState = "draft"   // being planned: its tasks wait for the operator's approval
	SessionRunning SessionState = "running" // approved: its Ready tasks are worked
	SessionPaused  SessionState = "paused"  // the operator's pause: nothing new is handed out
	SessionDone    SessionState = "done"
	SessionStopped SessionState = "stopped" // the kill switch: every claim was released
	SessionFailed  SessionState = "failed"
)

// closed says whether the state is an end: nothing more is added to the session.
func (st SessionState) closed() bool {
	return st == SessionDone || st == SessionStopped || st == SessionFailed
}

// Task kinds of a session's task.
const (
	KindTask  = "task"
	KindEpic  = "epic"  // groups tasks; never handed out
	KindCheck = "check" // a verifier's task
)

// Limits and defaults of a session (plan, sections 4 and 8.6).
const (
	DefaultSessionMaxTasks   = 200
	DefaultSessionMaxDepth   = 3
	DefaultSessionMaxRetries = 2
	MaxSessionTasksCeiling   = 10000 // the most an operator can raise max_tasks to
	MaxSessionDepthCeiling   = 10
	MaxSessionRetriesCeiling = 20
	MaxOpenSessions          = 50 // sessions of a profile that are not archived
	MaxSplitParts            = 20
	MaxArtifactsPerTask      = 100
	MaxAcceptanceBytes       = 8 << 10
	MaxGoalBytes             = 64 << 10
	DefaultAccessLevel       = "scoped"
)

// AccessLevels are the access levels a session may name: those of a chat.
var AccessLevels = []string{"scoped", "coding", "workspace", "full"}

// Session is one goal with its own tasks.
type Session struct {
	ID                int64         `json:"id"`
	ProfileID         string        `json:"profile_id"`
	ConversationID    string        `json:"conversation_id,omitempty"`
	Title             string        `json:"title"`
	Goal              string        `json:"goal"`
	State             SessionState  `json:"state"`
	AccessLevel       string        `json:"access_level"`
	BudgetUSD         float64       `json:"budget_usd"` // 0: no budget set
	SpentUSD          float64       `json:"spent_usd"`
	MaxTasks          int           `json:"max_tasks"`
	MaxDepth          int           `json:"max_depth"`
	MaxRetries        int           `json:"max_retries"`
	AutoCloseVerified bool          `json:"auto_close_verified"`
	CreatedBy         string        `json:"created_by"`
	ApprovedBy        string        `json:"approved_by,omitempty"`
	ApprovedAt        *time.Time    `json:"approved_at,omitempty"`
	Archived          bool          `json:"archived"`
	CreatedAt         time.Time     `json:"created_at"`
	UpdatedAt         time.Time     `json:"updated_at"`
	Rev               int64         `json:"rev"`
	Counts            SessionCounts `json:"counts"`
	ClientID          string        `json:"client_id,omitempty"`
	Deduped           bool          `json:"deduped,omitempty"` // set by CreateSession when the client id matched an existing session
}

// SessionCounts are a session's tasks per column, without the archived ones.
// Blocked and Verified are flags of tasks that sit in Review. Epics only group their
// children and never reach Done, so the columns, Done and Total leave them out (progress is
// done/total of real work); Epics counts them, and Stored (Total plus Epics) is what the
// session's max_tasks counts.
type SessionCounts struct {
	Counts
	Blocked  int `json:"blocked"`
	Verified int `json:"verified"`
	Total    int `json:"total"`
	Epics    int `json:"epics"`
}

// Stored is every live task of the session, epics included: what max_tasks bounds.
func (c SessionCounts) Stored() int { return c.Total + c.Epics }

// SessionInput is a new session. Only the operator may set AccessLevel,
// BudgetUSD, MaxTasks and MaxDepth: an agent that plans gets the defaults and
// the operator raises them.
type SessionInput struct {
	Title          string
	Goal           string
	ConversationID string
	AccessLevel    string
	BudgetUSD      float64
	MaxTasks       int
	MaxDepth       int
	ClientID       string // an idempotency key per profile: a repeat returns the existing session
}

// SessionUpdate changes what only the operator may. A nil field stays as it is.
type SessionUpdate struct {
	BudgetUSD         *float64
	MaxTasks          *int
	MaxDepth          *int
	MaxRetries        *int
	AccessLevel       *string
	AutoCloseVerified *bool
}

// SessionFilter narrows ListSessions.
type SessionFilter struct {
	State           SessionState // "" for every state
	IncludeArchived bool
}

// SessionTaskInput is a task added to a session. Acceptance is required.
type SessionTaskInput struct {
	Title             string
	Notes             string
	Acceptance        string
	Kind              string // "" for task
	ParentID          int64  // 0 for none
	DependsOn         []int64
	SuggestedExecutor string
	Assignee          string
}

// SplitPart is one of the tasks a task is split into.
type SplitPart struct {
	Title      string
	Acceptance string
	// Notes, when set, replace the notes the part would inherit from the task split.
	Notes string
}

// ArtifactInput is evidence of a task's work.
type ArtifactInput struct {
	Kind string
	Ref  string
	Note string
}

// Artifact is a stored ArtifactInput.
type Artifact struct {
	ID     int64     `json:"id"`
	TaskID int64     `json:"task_id"`
	Kind   string    `json:"kind"`
	Ref    string    `json:"ref"`
	Note   string    `json:"note"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
}

// SessionEvent is one change of a session.
type SessionEvent struct {
	ID    int64     `json:"id"`
	At    time.Time `json:"at"`
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"`
	Note  string    `json:"note"`
}

// SessionBoard is a session's board: its tasks in the five columns, as Board
// has them for the personal board.
type SessionBoard struct {
	Session Session           `json:"session"`
	Rev     int64             `json:"rev"`
	Counts  SessionCounts     `json:"counts"`
	Tasks   map[Status][]Task `json:"tasks"`
}

// SessionPlan is a session with all its tasks that are not archived, with
// their dependencies, in the order the plan was written.
type SessionPlan struct {
	Session Session `json:"session"`
	Tasks   []Task  `json:"tasks"`
}

// SessionTaskDetail is a session task with its history and evidence.
type SessionTaskDetail struct {
	Task      Task       `json:"task"`
	Events    []Event    `json:"events"`
	Artifacts []Artifact `json:"artifacts"`
}

func sessionNotFound(id int64) error { return fmt.Errorf("%w: session #%d", ErrNotFound, id) }
