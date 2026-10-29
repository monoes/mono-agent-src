package task

import (
	"github.com/monoes/mono-agent/internal/agenttokens"
	"github.com/monoes/mono-agent/internal/coderhost"
)

// personTokenEnv are the variables that carry an agent's token: a coder session's (the lead's),
// a mission's binding, and the task and session a worker is bound to. A process that holds one is
// an agent, whatever else its environment says, and an agent never acts as the person.
var personTokenEnv = []string{coderhost.SessionTokenEnv, agenttokens.EnvMissionToken, boundSessionEnv, boundTaskEnv}

// refuseTokenHolder refuses a person verb when the environment holds an agent token (even an
// empty one: its presence is the sign). It does not make the verbs proof against an agent that
// clears its environment; SECURITY.md says so.
func (d Deps) refuseTokenHolder(what string) error {
	for _, key := range personTokenEnv {
		if _, set := d.lookupEnv(key); set {
			return d.operatorOnlyError("%s is set, so a process holding an agent token is running this command; only the operator can %s; run it in your own terminal", key, what)
		}
	}
	return nil
}

func (d Deps) lookupEnv(key string) (string, bool) {
	v := d.Getenv(key)
	return v, v != ""
}
