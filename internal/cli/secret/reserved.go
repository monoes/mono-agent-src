package secret

import (
	"context"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/secrets"
)

// writeCtx lets the user's own command write the reserved vault entry
// (secrets.ReservedEntry, the HTTP API's bearer). An agent's shell, the chat
// and a workflow get no such context, and secrets refuses the write.
func writeCtx(ctx context.Context) context.Context {
	if operator.Refusal() == "" {
		return secrets.AllowReservedWrite(ctx)
	}
	return ctx
}

// writeErr is a write's error, with a refused write of the reserved entry
// given the operator-only exit (3, operator_only).
func writeErr(what string, err error) error {
	if errors.Is(err, secrets.ErrReservedEntry) {
		return clicore.OperatorOnly("%s: %v: %s; run it yourself in your terminal or the app", what, err, operator.Refusal())
	}
	return fmt.Errorf("%s: %w", what, err)
}
