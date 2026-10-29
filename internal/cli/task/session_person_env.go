package task

import (
	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// personTokenEnv are the variables that carry an agent's token: a coder session's (the lead's),
// a mission's binding, and the task and session a worker is bound to. A process that holds one is
// an agent, whatever else its environment says, and an agent never acts as the person.
var personTokenEnv = []string{coderhost.SessionTokenEnv, agenttokens.EnvMissionToken, boundSessionEnv, boundTaskEnv}

// refuseTokenHolder refuses a person verb when the environment holds an agent token, and when the
// process runs inside the process tree of a coder turn (the ancestry check that binds a session
// token to its turn, applied here without a token). A variable set to the empty string does not
// count (lookupEnv wants a value). Both are a courtesy layer, not proof: an agent that clears its
// environment AND detaches from its turn's process tree is not seen; SECURITY.md says so.
func (d Deps) refuseTokenHolder(what string) error {
	for _, key := range personTokenEnv {
		if _, set := d.lookupEnv(key); set {
			return d.operatorOnlyError("%s is set, so a process holding an agent token is running this command; only the operator can %s; run it in your own terminal", key, what)
		}
	}
	db, err := d.OpenDB()
	if err != nil {
		return err
	}
	defer db.Close()
	in, err := coderhost.NewStore(db.DB).CallerInATurn()
	if err != nil {
		return err // fails closed: a process that cannot be placed is not let through
	}
	if in {
		return d.operatorOnlyError("this command runs inside a coder session's turn, so a model is running it; only the operator can %s; run it in your own terminal", what)
	}
	return nil
}

func (d Deps) lookupEnv(key string) (string, bool) {
	v := d.Getenv(key)
	return v, v != ""
}
