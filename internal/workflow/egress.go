package workflow

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/monoes/mono-agent/internal/secrets"
)

// Egress says what a node type can send over the network, for the grant's
// host binding (`secret agent-access on --host`). In a run that carries a
// secret read under such a grant, every send must go to a host the grant
// names; a node type's egress says whether the engine can check that.
type Egress int

const (
	// EgressUnchecked is a node type that does network I/O to somewhere the
	// engine cannot derive from its config (a vendor's fixed API host, a
	// browser, a provider call, a shell). The zero value, so a type nobody
	// classified is this one. In a run that carries a host-bound grant's
	// secret it is refused.
	EgressUnchecked Egress = iota
	// EgressNone is a node type with no network I/O (pure data, control).
	EgressNone
	// EgressChecked is a node type whose destinations the engine derives from
	// the resolved config (destination keys, a connection string's hosts, a
	// URL value) and holds to the grant's hosts before it runs.
	EgressChecked
)

func (e Egress) String() string {
	return [...]string{"unchecked", "none", "checked"}[e]
}

type egressFunc func(nodeType string) (Egress, bool)

var egressClassifier atomic.Pointer[egressFunc]

// SetEgressClassifier installs the process's classification of node types
// (internal/noderegistry's table); nil removes it.
func SetEgressClassifier(f func(nodeType string) (Egress, bool)) {
	if f == nil {
		egressClassifier.Store(nil)
		return
	}
	g := egressFunc(f)
	egressClassifier.Store(&g)
}

// EgressOf is nodeType's classification; a type nobody classified is
// EgressUnchecked.
func EgressOf(nodeType string) Egress {
	if f := egressClassifier.Load(); f != nil {
		if e, ok := (*f)(nodeType); ok {
			return e
		}
	}
	return EgressUnchecked
}

// CheckEgress is the engine's one egress check, called by the shared resolver
// (resolveNodeConfig) for every node config, however it came to be resolved:
// the batch, each item of a per-item run, a loop's iteration, a sub-node or
// tool call, a trigger source. keys are the executor's destination keys.
//
// Only a run that carries a secret read under a grant that names hosts is
// held (secrets.HostBoundCarry): an approved workflow, no grant and
// --any-host are unchanged. It can refuse a node that did not itself read a
// secret, when an earlier step of the run did: that is CheckSendHost's rule
// for http and mcp, now for every node type.
func CheckEgress(ctx context.Context, nodeType string, keys [][]string, resolved map[string]interface{}) error {
	// The http nodes, mcp.call and trigger.sse check every hop at the send.
	if !secrets.UnderAgentAccess(ctx) || secrets.ChecksHostAtSend(nodeType) {
		return nil
	}
	switch EgressOf(nodeType) {
	case EgressNone:
		return nil
	case EgressUnchecked:
		why, err := secrets.HostBoundCarry(ctx, sendTexts(resolved, nil)...)
		if err != nil {
			return err
		}
		if why != "" {
			return fmt.Errorf("%w: a %s step sends to somewhere the grant's host list cannot check (%s); use agent-access --any-host, or keep this step out of a run that reads secrets", secrets.ErrHostNotAllowed, nodeType, why)
		}
		return nil
	}
	var texts []string
	for _, d := range egressDestinations(keys, resolved) {
		if texts == nil {
			texts = sendTexts(resolved, []string{})
		}
		if err := secrets.CheckSendHost(ctx, d.value, texts...); err != nil {
			return fmt.Errorf("%q: %w", d.field, err)
		}
	}
	return nil
}
