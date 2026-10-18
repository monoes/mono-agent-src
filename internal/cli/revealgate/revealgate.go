// Package revealgate refuses the CLI commands that print a decrypted secret
// (a vault entry's fields, the vault's export and its passphrase, an OAuth
// client secret, the profile's ID, bank and card numbers, a run's
// unredacted outputs) to whatever is not the operator's own terminal: an
// agent, the chat or a workflow could otherwise read every secret the vault
// holds. Names, masked values and metadata stay open to them.
package revealgate

import (
	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
)

// Refuse refuses what, a command that prints decrypted secrets, when this
// process is not the operator's own terminal (operator.Refusal): exit 3 and
// the code operator_only, with the reason. It is nil for the operator.
func Refuse(what string) error {
	why := operator.Refusal()
	if why == "" {
		return nil
	}
	return clicore.OperatorOnly("%s prints decrypted secrets, which only the user reads: %s; run it yourself in your terminal or the app", what, why)
}
