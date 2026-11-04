// Package revealgate refuses the CLI commands that print a decrypted secret
// (a vault entry's fields, the vault's export and its passphrase, an OAuth
// client secret, the profile's ID, bank and card numbers, a run's
// unredacted outputs) to whatever is not the operator's own terminal: an
// agent, the chat or a workflow could otherwise read every secret the vault
// holds. Names, masked values and metadata stay open to them.
package revealgate

import (
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
)

// Refuse refuses what, a command that prints decrypted secrets, when this
// process is not the operator's own terminal (opgate.Refusal: the environment
// markers, an agent token, a process inside a coder session's turn): exit 3 and
// the code operator_only, with the reason. It is nil for the operator.
func Refuse(what string) error { return refuse(what, opgate.Refusal()) }

// RefuseFor is Refuse for a command that has an agent path next to the operator's (a reveal under
// the profile's Reveal access level): the caller asks opgate.Refusal once, takes the agent path
// when it is not empty, and hands the same answer here when the level does not allow the reveal,
// so the decision and the refusal text cannot disagree. A process inside a coder turn that cleared
// its markers, an agent token holder and a process that cannot be placed are on the agent path
// like a marked agent.
func RefuseFor(what, why string) error { return refuse(what, why) }

func refuse(what, why string) error {
	if why == "" {
		return nil
	}
	return clicore.OperatorOnly("%s prints decrypted secrets, which only the user reads: %s; run it yourself in your terminal or the app", what, why)
}
