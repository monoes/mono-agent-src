package task

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/tasks"
)

// personTokenEnv are the variables that carry an agent's token: a coder session's (the lead's),
// a mission's binding, and the task and session a worker is bound to. A process that holds one is
// an agent, whatever else its environment says, and an agent never acts as the person.
var personTokenEnv = []string{coderhost.SessionTokenEnv, agenttokens.EnvMissionToken, boundSessionEnv, boundTaskEnv}

// personGate is the whole gate of a person verb, in this order: the environment holds no agent token
// (read before anything else, so a mission token is not even looked up in the database), the caller
// is the operator, and the process is not inside a coder turn. Both are a courtesy layer, not
// proof: an agent that clears its environment AND detaches from its turn's process tree is not
// seen; SECURITY.md says so. A variable set to the empty string does not count (lookupEnv wants a value).
func (d Deps) personGate(cmd *cobra.Command, what string) (tasks.Actor, error) {
	for _, key := range personTokenEnv {
		if _, set := d.lookupEnv(key); set {
			return tasks.Actor{}, d.operatorOnlyError("%s is set, so a process holding an agent token is running this command; only the operator can %s; run it in your own terminal", key, what)
		}
	}
	actor, err := d.callerFor(flagAs(cmd)).operator(what)
	if err != nil {
		return tasks.Actor{}, err
	}
	db, err := d.OpenDB()
	if err != nil {
		return tasks.Actor{}, err
	}
	defer db.Close()
	in, err := coderhost.NewStore(db.DB).CallerInATurn()
	if err != nil {
		return tasks.Actor{}, err // fails closed: a process that cannot be placed is not let through
	}
	if in {
		return tasks.Actor{}, d.operatorOnlyError("this command runs inside a coder session's turn, so a model is running it; only the operator can %s; run it in your own terminal", what)
	}
	return actor, nil
}

func (d Deps) lookupEnv(key string) (string, bool) {
	v := d.Getenv(key)
	return v, v != ""
}
