package inputs

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

type Reason string

const (
	ReasonRunning           Reason = "running"
	ReasonNoSession         Reason = "no-session"
	ReasonNotMission        Reason = "not-mission"
	ReasonSessionNotRunning Reason = "session-not-running"
	ReasonNotStarted        Reason = "not-started"
	ReasonPaused            Reason = "paused"
	ReasonUnreadable        Reason = "unreadable"
)

type Run struct {
	Running bool
	Why     Reason
}

type TaskCounts struct {
	Total, Inbox, Ready, InProgress, Review, Done, Archived, Unknown, Blocked int
}

func (c TaskCounts) Open() int { return 0 }

type BudgetShare struct {
	Known               bool
	Share               float64
	BudgetUSD, SpentUSD float64
}

type EpochFacts struct {
	Count              int64
	HasLatest          bool
	Latest             types.Epoch
	ParkedFinishRounds int
}

type Snapshot struct {
	Run         Run
	Tasks       TaskCounts
	Directions  []types.Direction
	Epochs      EpochFacts
	EvidenceSeq int64
	Budget      BudgetShare
}

type Inputs interface {
	Check(ctx context.Context, session int64) (Run, error)
	Snapshot(ctx context.Context, session int64) (Snapshot, error)
}

type Reader struct{ db *sql.DB }

func New(db *sql.DB) *Reader { return &Reader{db: db} }

func (r *Reader) Check(ctx context.Context, session int64) (Run, error) {
	return Run{Running: true}, nil
}

func (r *Reader) Snapshot(ctx context.Context, session int64) (Snapshot, error) {
	return Snapshot{Run: Run{Running: true}}, nil
}

func (r *Reader) Running(ctx context.Context, profile string, gen int64) (bool, error) {
	return true, nil
}

type SessionRun struct{}

func (r *Reader) RunOf(session int64) SessionRun { return SessionRun{} }

func (SessionRun) Running(ctx context.Context) (bool, error) { return true, nil }
