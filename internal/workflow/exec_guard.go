package workflow

import (
	"context"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/secrets"
)

// ExecGuard sees every node before it runs. When it reports handled, its
// outputs and error stand in for the node's own and the node does not run.
// Outbound sends use it to wait for a person's approval (internal/hilsend).
type ExecGuard func(ctx context.Context, nodeType string, in NodeInput, config map[string]interface{}) (out []NodeOutput, handled bool, err error)

var execGuard atomic.Pointer[ExecGuard]

// SetExecGuard installs g for the process; nil removes it.
func SetExecGuard(g ExecGuard) {
	if g == nil {
		execGuard.Store(nil)
		return
	}
	execGuard.Store(&g)
}

// RunExecGuard runs the installed guard, if any.
func RunExecGuard(ctx context.Context, nodeType string, in NodeInput, config map[string]interface{}) ([]NodeOutput, bool, error) {
	if g := execGuard.Load(); g != nil {
		return (*g)(ctx, nodeType, in, config)
	}
	return nil, false, nil
}

// Declarer is what an executor says about whether it publishes to third
// parties: false when it sends nothing off this machine. An executor that
// does not declare it publishes (fail closed; the pin is in noderegistry).
type Declarer interface{ PublishesToThirdParty() bool }

// Publishes reports whether e publishes to third parties.
func Publishes(e NodeExecutor) bool {
	if d, ok := e.(Declarer); ok {
		return d.PublishesToThirdParty()
	}
	return true
}

// RunExecGuardFor is the seam every dispatch point passes through. Under
// agent access a node that publishes is refused, before the guard and so
// before any approval card, when its run has read a granted secret
// (secrets.CheckPublish); --any-host does not lift it. Everything else is
// RunExecGuard's. A run that is not under agent access passes unchanged.
func RunExecGuardFor(ctx context.Context, e NodeExecutor, in NodeInput, config map[string]interface{}) ([]NodeOutput, bool, error) {
	if err := secrets.CheckPublish(ctx, e.Type(), Publishes(e)); err != nil {
		return nil, true, err
	}
	return RunExecGuard(ctx, e.Type(), in, config)
}
