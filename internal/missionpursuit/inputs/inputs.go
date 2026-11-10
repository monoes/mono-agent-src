package inputs

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// Reason says why a pursuit is or is not running.
type Reason string

const (
	ReasonRunning           Reason = "running"
	ReasonNoSession         Reason = "no-session"          // no such task session
	ReasonNotMission        Reason = "not-mission"         // the session is not a mission
	ReasonSessionNotRunning Reason = "session-not-running" // task_sessions.state is anything but 'running'
	ReasonNotStarted        Reason = "not-started"         // no mission_pursuit row, or one that is not enabled
	ReasonPaused            Reason = "paused"              // the person paused the pursuit
	ReasonUnreadable        Reason = "unreadable"          // the rows could not be read: not running, with an error
)

// Run is the run state of one pursuit. Running is true only for ReasonRunning.
type Run struct {
	Running bool
	Why     Reason
}

// TaskCounts are the session's tasks per board status.
type TaskCounts struct {
	Total      int
	Inbox      int
	Ready      int
	InProgress int
	Review     int
	Done       int
	Archived   int
	Unknown    int // a status this code does not know
	Blocked    int // open tasks (not done, not archived) flagged blocked
	// Workable counts the tasks the lead can still move: not an epic, not done or archived, not blocked, and not verified
	// and waiting in review (the settled test of the mission close). Zero means the mission has no unsettled work.
	Workable int
}

// Open is the number of tasks not yet done or archived.
func (c TaskCounts) Open() int { return c.Inbox + c.Ready + c.InProgress + c.Review + c.Unknown }

// BudgetShare is the session's spend against its budget. Known false: the budget is not a positive finite number or the
// spend is negative or not finite; Share is then 0 and no trigger may be read from it.
type BudgetShare struct {
	Known     bool
	Share     float64 // spent / budget; above 1 when over budget
	BudgetUSD float64
	SpentUSD  float64
}

// EpochFacts are the strategy epochs of the session.
type EpochFacts struct {
	Count              int64 // the latest sequence (epochs are numbered 1..n)
	HasLatest          bool
	Latest             types.Epoch
	ParkedFinishRounds int // epochs that started because a finish was parked
}

// Snapshot is everything read for one session in one call. When Run.Running is false every other field is zero.
type Snapshot struct {
	Run         Run
	Tasks       TaskCounts
	Directions  []types.Direction // open: proposed, active, stalled
	Epochs      EpochFacts
	EvidenceSeq int64
	Budget      BudgetShare
}

// Inputs is what the scorer and the host loop consume. An error always comes with Running false.
type Inputs interface {
	Check(ctx context.Context, session int64) (Run, error)
	Snapshot(ctx context.Context, session int64) (Snapshot, error)
}

// Reader implements Inputs over the host database. It only reads.
type Reader struct{ db *sql.DB }

// New builds a Reader over db.
func New(db *sql.DB) *Reader { return &Reader{db: db} }
