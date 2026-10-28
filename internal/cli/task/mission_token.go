package task

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/taskbind"
	"github.com/monoes/mono-agent/internal/tasks"
)

// missionBinding reads what a process may act on from the mission token it carries
// (agenttokens.EnvMissionToken), checked against the database and the profile of this call.
// With no token the process is bound to nothing; with one that does not verify (unknown,
// expired, revoked, of another profile) it is bound to nothing that exists, which refuses every
// act. The labels in the environment (MONOAGENT_TASK_*) never bind.
func (d Deps) missionBinding() taskbind.Binding {
	token := d.Getenv(agenttokens.EnvMissionToken)
	if token == "" {
		return taskbind.Binding{State: taskbind.None}
	}
	if d.OpenDB == nil || d.ProfileID == nil {
		return taskbind.Binding{State: taskbind.Invalid} // a caller that cannot look it up trusts nothing
	}
	db, err := d.OpenDB()
	if err != nil {
		return taskbind.Binding{State: taskbind.Invalid}
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := tasks.NewStore(db.DB).Profile(ctx, d.ProfileID())
	if err != nil {
		return taskbind.Binding{State: taskbind.Invalid}
	}
	b := taskbind.Resolve(ctx, db.DB, p.ID, token)
	if b.Outside {
		fmt.Fprintln(os.Stderr, "task: refused: "+b.Err.Error())
	}
	return b
}
