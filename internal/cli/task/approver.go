package task

import (
	"github.com/spf13/cobra"

	"github.com/monoes/mono-agent/internal/tasks"
)

// approver is who runs `session approve`, resolved one way from every token the process may hold.
// The operator is the caller no agent marker or token makes an agent. An agent is the named caller
// whose binding comes from its mission token (callerFor: session, task, role; a worker or verifier
// then fails the store's rule) with the chat added from its verified coder-session token (ownChat).
// The store takes only this Actor, never a label from the environment: Conversation is set here and
// by the in-process mission lead (dynorg) from trusted code, nowhere else.
func (d Deps) approver(cmd *cobra.Command) (tasks.Actor, error) {
	caller := d.callerFor(flagAs(cmd))
	if !caller.isAgent() {
		// The operator's branch: the shared gate (tokens, operator, not inside a coder turn).
		return d.personGate(cmd, "approve a session's plan")
	}
	if caller.asBlank || caller.envBlank || caller.actor.Name == "" {
		// An agent that has not named itself is refused as an agent.
		return caller.operator("approve a session's plan")
	}
	actor, err := d.namedAgent(caller)
	if err != nil {
		return tasks.Actor{}, err
	}
	if actor.Conversation, err = d.ownChat(); err != nil {
		return tasks.Actor{}, err
	}
	return actor, nil
}
