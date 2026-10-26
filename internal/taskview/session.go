package taskview

import (
	"time"

	"github.com/monoes/mono-agent/internal/tasks"
)

// SessionView is a task session as the task tools return it: the title and the goal were
// written by the operator or an agent, so they are _untrusted.
type SessionView struct {
	ID                int64               `json:"id"`
	ProfileID         string              `json:"profile_id"`
	ConversationID    string              `json:"conversation_id,omitempty"`
	TitleUntrusted    string              `json:"title_untrusted"`
	GoalUntrusted     string              `json:"goal_untrusted"`
	State             tasks.SessionState  `json:"state"`
	AccessLevel       string              `json:"access_level"`
	BudgetUSD         float64             `json:"budget_usd"`
	SpentUSD          float64             `json:"spent_usd"`
	MaxTasks          int                 `json:"max_tasks"`
	MaxDepth          int                 `json:"max_depth"`
	MaxRetries        int                 `json:"max_retries"`
	AutoCloseVerified bool                `json:"auto_close_verified"`
	CreatedBy         string              `json:"created_by"`
	ApprovedBy        string              `json:"approved_by,omitempty"`
	ApprovedAt        *time.Time          `json:"approved_at,omitempty"`
	Archived          bool                `json:"archived"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
	Rev               int64               `json:"rev"`
	Counts            tasks.SessionCounts `json:"counts"`
}

// SessionOf is the view of a session.
func SessionOf(s tasks.Session) SessionView {
	return SessionView{
		ID: s.ID, ProfileID: s.ProfileID, ConversationID: s.ConversationID, TitleUntrusted: s.Title, GoalUntrusted: s.Goal,
		State: s.State, AccessLevel: s.AccessLevel, BudgetUSD: s.BudgetUSD, SpentUSD: s.SpentUSD, MaxTasks: s.MaxTasks,
		MaxDepth: s.MaxDepth, MaxRetries: s.MaxRetries, AutoCloseVerified: s.AutoCloseVerified, CreatedBy: s.CreatedBy,
		ApprovedBy: s.ApprovedBy, ApprovedAt: s.ApprovedAt, Archived: s.Archived, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
		Rev: s.Rev, Counts: s.Counts,
	}
}

// SessionEventView is one event of a session's history; its note may carry a task's title.
type SessionEventView struct {
	ID            int64     `json:"id"`
	At            time.Time `json:"at"`
	Actor         string    `json:"actor"`
	Kind          string    `json:"kind"`
	NoteUntrusted string    `json:"note_untrusted"`
}

// SessionEventViews are the views of a session's events.
func SessionEventViews(es []tasks.SessionEvent) []SessionEventView {
	out := make([]SessionEventView, 0, len(es))
	for _, e := range es {
		out = append(out, SessionEventView{ID: e.ID, At: e.At, Actor: e.Actor, Kind: e.Kind, NoteUntrusted: e.Note})
	}
	return out
}

// ArtifactView is a task's evidence; the reference and the note come from the worker.
type ArtifactView struct {
	ID            int64     `json:"id"`
	TaskID        int64     `json:"task_id"`
	Kind          string    `json:"kind"`
	RefUntrusted  string    `json:"ref_untrusted"`
	NoteUntrusted string    `json:"note_untrusted"`
	At            time.Time `json:"at"`
	Actor         string    `json:"actor"`
}

// ArtifactOf is the view of an artifact.
func ArtifactOf(a tasks.Artifact) ArtifactView {
	return ArtifactView{ID: a.ID, TaskID: a.TaskID, Kind: a.Kind, RefUntrusted: a.Ref, NoteUntrusted: a.Note, At: a.At, Actor: a.Actor}
}
