package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// timeFmt is the one fixed-width UTC format every stored time uses, so that
// comparing the text compares the times.
const timeFmt = "2006-01-02T15:04:05Z"

// Status is a task's column.
type Status string

const (
	StatusInbox      Status = "inbox"
	StatusReady      Status = "ready"
	StatusInProgress Status = "in_progress"
	StatusReview     Status = "review"
	StatusDone       Status = "done"
	StatusArchived   Status = "archived"
)

// BoardStatuses are the five columns, in board order. Archived is hidden.
var BoardStatuses = []Status{StatusInbox, StatusReady, StatusInProgress, StatusReview, StatusDone}

// IsBoard reports whether s is one of the five columns.
func (s Status) IsBoard() bool {
	for _, b := range BoardStatuses {
		if s == b {
			return true
		}
	}
	return false
}

// ParseStatus reads a status name; "progress" and "in-progress" mean in_progress.
func ParseStatus(name string) (Status, error) {
	n := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "-", "_")
	if n == "progress" {
		n = string(StatusInProgress)
	}
	switch st := Status(n); st {
	case StatusInbox, StatusReady, StatusInProgress, StatusReview, StatusDone, StatusArchived:
		return st, nil
	}
	return "", invalid("unknown status %q (use inbox, ready, in_progress, review, done or archived)", echo(name))
}

// Where a task came from.
const (
	SourceCLI    = "cli"
	SourceApp    = "app"
	SourceChrome = "chrome"
	SourceOS     = "os"
	SourceAgent  = "agent"
)

// validSource reports whether kind is one of the stored source kinds.
func validSource(kind string) bool {
	switch kind {
	case SourceCLI, SourceApp, SourceChrome, SourceOS, SourceAgent:
		return true
	}
	return false
}

// ActorKind says what is acting: the operator, an agent, or a capture surface.
type ActorKind int

const (
	Human ActorKind = iota + 1
	Agent
	Capture
)

// Actor is who does something to a board. A Capture's Name is its surface:
// empty, "chrome" or "os". An Agent's Name is the name it holds claims under; it
// may not be a label the store writes for others (you, agent, capture, chrome,
// os), in any case.
//
// Session, when not 0, binds the actor to one task session (a worker's token
// carries it): it may act on that session's tasks and on nothing else, not on
// the personal board and not on another session.
//
// Task, when not 0, narrows that binding to one task of the session (a mission worker's
// token carries it): the actor may claim, comment on, finish, release and attach evidence
// to that task and to no other, and adds no task of its own.
//
// Verifier, with Task, makes the binding a verifier's: the actor may record a verdict on that
// one task (once it is in Review), comment on it there and attach evidence to it, and may
// not claim, finish, release, split or assign anything. A Verifier without a Task is refused.
// A Task binding without Verifier never records a verdict, on its own task or another.
type Actor struct {
	Kind     ActorKind
	Name     string
	Session  int64
	Task     int64
	Verifier bool
}

// Label is how the actor is written in an event and in a claim.
func (a Actor) Label() string {
	switch {
	case a.Kind == Human:
		return "you"
	case a.Name != "":
		return a.Name
	case a.Kind == Agent:
		return "agent"
	}
	return "capture"
}

// Limits (spec 4.6 and 5.2).
const (
	MaxTitleRunes       = 200
	DerivedTitleRunes   = 120
	MaxNotesBytes       = 64 << 10
	MaxCommentBytes     = 8 << 10
	MaxURLBytes         = 2048
	MaxSourceTitleRunes = 200
	MaxAppRunes         = 100
	MaxNameLen          = 64
	MaxClientIDLen      = 64
	MaxEventsPerTask    = 500
	MaxEventsToClaim    = 2000
	MaxOpenTasks        = 2000
	DefaultListLimit    = 500
	MaxListLimit        = 2000
	DefaultLease        = 30 * time.Minute
	MaxLease            = 24 * time.Hour
	positionGap         = 1024
)

// Source says where a task came from.
type Source struct {
	Kind  string `json:"kind"`
	URL   string `json:"url"`
	Title string `json:"title"`
	App   string `json:"app"`
}

// Claim is an agent's hold on an in-progress task.
type Claim struct {
	By    string    `json:"by"`
	Until time.Time `json:"until"`
	Stale bool      `json:"stale"`
}

// LastEvent is the newest event of a task.
type LastEvent struct {
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"`
	At    time.Time `json:"at"`
}

// Task is a card on a profile's board. Its Author is who added it, as its
// created event names them: you, an agent's name, chrome or os, and for what
// is not the operator's own terminal chat, workflow:<execution id> or
// agent:<kind>.
type Task struct {
	ID        int64      `json:"id"`
	ProfileID string     `json:"profile_id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	Status    Status     `json:"status"`
	Position  int64      `json:"position"`
	Source    Source     `json:"source"`
	Author    string     `json:"author"`
	Claim     *Claim     `json:"claim"`
	LastEvent *LastEvent `json:"last_event"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`

	// Of a task of a session (session_id is not NULL); all are left out of the
	// JSON of a personal task, which reads as it always did.
	SessionID         int64   `json:"session_id,omitempty"`
	Kind              string  `json:"kind,omitempty"`
	ParentID          int64   `json:"parent_id,omitempty"`
	Acceptance        string  `json:"acceptance,omitempty"`
	Assignee          string  `json:"assignee,omitempty"`
	SuggestedExecutor string  `json:"suggested_executor,omitempty"`
	Blocked           bool    `json:"blocked,omitempty"`
	Verified          bool    `json:"verified,omitempty"`
	Retries           int     `json:"retries,omitempty"`
	DependsOn         []int64 `json:"depends_on,omitempty"`
}

// Event is one change of a task.
type Event struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Kind       string    `json:"kind"`
	FromStatus string    `json:"from_status"`
	ToStatus   string    `json:"to_status"`
	Note       string    `json:"note"`
}

// Profile is the profile a board belongs to.
type Profile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Counts are a board's cards per column; Stale counts claims past their lease.
type Counts struct {
	Inbox      int `json:"inbox"`
	Ready      int `json:"ready"`
	InProgress int `json:"in_progress"`
	Review     int `json:"review"`
	Done       int `json:"done"`
	Stale      int `json:"stale"`
}

// Board is a whole profile's board, read in one snapshot.
type Board struct {
	Profile Profile           `json:"profile"`
	Rev     int64             `json:"rev"`
	Counts  Counts            `json:"counts"`
	Tasks   map[Status][]Task `json:"tasks"`
}

// Filter narrows List. No statuses means the default of the caller and a Limit
// that is not above 0 means the default limit: List says what both are.
type Filter struct {
	Statuses  []Status
	Source    string
	ClaimedBy string
	Stale     bool
	Limit     int
}

// AddInput is a new task. A Title may come with Notes or with Text (which then
// becomes the notes); Text alone gives the task its title from its first line.
// Notes and Text together are refused (spec 4.6).
type AddInput struct {
	Title       string
	Notes       string // only with a Title
	Text        string // captured or typed text
	Ready       bool   // straight to Ready: the operator only
	SourceKind  string // "", cli, app, chrome or os: the actor decides what is allowed
	SourceURL   string
	SourceTitle string
	SourceApp   string
	ClientID    string // an idempotency key from a capture surface
}

// Edit changes a task's text. A nil field stays as it is.
type Edit struct {
	Title *string
	Notes *string
}

// Placement says where a moved card goes in its new column. At most one field
// may be set. None means the column's default end (the top of Inbox, Review and
// Done, the bottom of Ready and In progress) for a card that is new to the
// column; a card that is in it already stays where it is.
type Placement struct {
	Before, After int64 // the id of a card in the target column
	Top, Bottom   bool
}

func (p Placement) set() int {
	n := 0
	for _, b := range []bool{p.Before != 0, p.After != 0, p.Top, p.Bottom} {
		if b {
			n++
		}
	}
	return n
}

// Outcome is how an agent hands a task back: a result, or a question.
type Outcome struct {
	Result   string
	Question string
}

var (
	ErrNotFound     = errors.New("task not found")
	ErrInvalid      = errors.New("invalid input")
	ErrOperatorOnly = errors.New("only the operator can do that")
	ErrNotReady     = errors.New("task cannot be claimed")
	ErrClaimed      = errors.New("task is claimed")
	ErrNotClaimant  = errors.New("you do not hold this task")
	ErrLimit        = errors.New("limit reached")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func operatorOnly(what string) error { return fmt.Errorf("%w: %s", ErrOperatorOnly, what) }

func notReady(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotReady, fmt.Sprintf(format, a...))
}

func notClaimant(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotClaimant, fmt.Sprintf(format, a...))
}

func limit(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrLimit, fmt.Sprintf(format, a...))
}

// ClaimedError says who holds a task and until when. It matches ErrClaimed.
type ClaimedError struct {
	By    string
	Until time.Time
}

func (e *ClaimedError) Error() string {
	return fmt.Sprintf("task is claimed by %s until %s", e.By, e.Until.UTC().Format(time.RFC3339))
}

func (e *ClaimedError) Is(target error) bool { return target == ErrClaimed }
