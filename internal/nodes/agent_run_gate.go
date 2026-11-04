package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/automation"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/workflow"
)

// A browser action of a package that isn't built-in runs only for the user
// (#898). A recorded package (`record save`), a local one (a folder under
// ~/.monoagent/actions) and an imported one are definitions anyone with the
// user's files can write, and an action runs with the full action language
// (clicks, page scripts, requests) in the user's logged-in browser, where no
// send card and no agent browser level applies. An agent, the chat and a
// workflow the user hasn't approved get ErrUserOnlyBrowserRun instead of the
// run, before a tab opens. Built-in packages run as before; their sends wait
// for approval (internal/hilsend).

// ErrUserOnlyBrowserRun is the refusal of a browser action only the user
// runs (BrowserNode.RunRefusal).
var ErrUserOnlyBrowserRun = errors.New("only the user runs a browser action of a package that isn't built-in")

// RunRefusal is why ctx may not run this node, wrapping
// ErrUserOnlyBrowserRun, or nil when it may. `node run` asks before it
// connects a browser; Execute asks again before it opens a tab.
func (b *BrowserNode) RunRefusal(ctx context.Context) error {
	trust := automationTrust(strings.ToLower(b.platform))
	if trust == automation.TrustBuiltin {
		return nil
	}
	why := operator.Refusal()
	if why == "" {
		// a command that ran this node and found its process an agent's without a marker (workflow.WithAgentCaller)
		why = workflow.AgentCallerFrom(ctx)
	}
	if why == "" && workflow.AIAuthoredFrom(ctx) {
		why = "it runs as a step the user hasn't approved"
	}
	if why == "" {
		if _, ok := workflow.CalledByUnapproved(workflow.TriggerDataFrom(ctx)); ok {
			why = "a workflow the user hasn't approved called the workflow that runs it"
		}
	}
	if why == "" {
		return nil
	}
	if trust == "" {
		trust = "unknown"
	}
	return fmt.Errorf("%w: %s is a %s package, whose actions run in the user's logged-in browser as written in its files, so only the user runs %s: %s; "+
		"the user runs it in their own terminal, the app, or a workflow they saved", ErrUserOnlyBrowserRun, b.platform, trust, b.Type(), why)
}
