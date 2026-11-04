package secret

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/monoes/mono-agent/internal/cli/clicore"
	"github.com/monoes/mono-agent/internal/cli/opgate"
	"github.com/monoes/mono-agent/internal/secrets"
)

// writeCtx lets the user's own command write the reserved vault entry
// (secrets.ReservedEntry, the HTTP API's bearer). An agent's shell, the chat,
// a workflow and a process inside a coder turn (whatever its environment says:
// opgate.Refusal) get no such context, and secrets refuses the write.
func writeCtx(ctx context.Context) context.Context {
	if opgate.Refusal() == "" {
		return secrets.AllowReservedWrite(ctx)
	}
	return ctx
}

// writeErr is a write's error, with a refused write of the reserved entry
// given the operator-only exit (3, operator_only).
func writeErr(what string, err error) error {
	if errors.Is(err, secrets.ErrReservedEntry) {
		why := opgate.Refusal()
		if opgate.Unplaced(why) { // it may be the user, for whom "run it yourself" cannot succeed
			return clicore.OperatorOnly("%s: %v: %s; retry, or repair the database", what, err, why)
		}
		return clicore.OperatorOnly("%s: %v: %s; run it yourself in your terminal or the app (its bearer is the field token: --field token=<value>)",
			what, err, why)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// warnTokenless warns the user whose own write of the reserved entry leaves
// it without the field the server reads: the API would not take it.
func warnTokenless(w io.Writer, name string, fields map[string]string) {
	if secrets.IsReservedEntry(name) && fields != nil && fields[secrets.ReservedTokenField] == "" {
		fmt.Fprintf(w, "Warning: the server reads %s's bearer from its field %q, which this write leaves out: use --field %s=<value>\n",
			secrets.ReservedEntry, secrets.ReservedTokenField, secrets.ReservedTokenField)
	}
}
