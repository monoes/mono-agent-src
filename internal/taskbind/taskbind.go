// Package taskbind turns the mission token a worker or verifier process carries into the
// binding of a tasks.Actor. It is the one place the task commands and the task tools learn
// which session, task and role a process is bound to: from a token the database verifies,
// never from a label in the process's own environment.
package taskbind

import (
	"context"
	"database/sql"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/tasks"
)

// State says what the process presented.
type State int

const (
	// None: no token. The process is an ordinary agent, bound to nothing.
	None State = iota
	// Valid: a live mission token of this profile.
	Valid
	// Invalid: a token that is unknown, expired, revoked, of another profile or not a mission
	// token. The process holds a binding to nothing that exists, which refuses every act.
	Invalid
)

// Binding is what a verified token binds a process to.
type Binding struct {
	State    State
	Name     string
	Session  int64
	Task     int64
	Verifier bool
}

// Resolve reads presented (the value of agenttokens.EnvMissionToken) against the database.
// profileID is the profile the call runs on.
func Resolve(ctx context.Context, db *sql.DB, profileID, presented string) Binding {
	if presented == "" {
		return Binding{State: None}
	}
	t, err := agenttokens.NewStore(db).VerifyMission(ctx, profileID, presented)
	if err != nil || t.Mission == nil {
		return Binding{State: Invalid}
	}
	m := t.Mission
	return Binding{State: Valid, Name: m.Actor, Session: m.Session, Task: m.Task, Verifier: m.Verifier()}
}

// Apply sets the binding on the actor. A token binds the name too: the process cannot act
// under another worker's name. An invalid token binds to nothing that exists (-1).
func (b Binding) Apply(a tasks.Actor) tasks.Actor {
	switch b.State {
	case Valid:
		a.Name, a.Session, a.Task, a.Verifier = b.Name, b.Session, b.Task, b.Verifier
	case Invalid:
		a.Session, a.Task, a.Verifier = -1, -1, false
	}
	return a
}
