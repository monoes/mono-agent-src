package workflow

import (
	"context"
	"strings"
)

type triggerKey struct{}

type triggerInfo struct {
	triggerType string
	data        map[string]interface{}
}

// WithTrigger attaches an execution's trigger type and trigger data to ctx,
// so a node can read what started the run even when the nodes before it did
// not pass it on (org nodes continue the chain trace from it).
func WithTrigger(ctx context.Context, triggerType string, data map[string]interface{}) context.Context {
	return context.WithValue(ctx, triggerKey{}, triggerInfo{triggerType: triggerType, data: data})
}

// TriggerDataFrom returns the trigger data WithTrigger attached, or nil.
// Callers must not modify it.
func TriggerDataFrom(ctx context.Context) map[string]interface{} {
	info, _ := ctx.Value(triggerKey{}).(triggerInfo)
	return info.data
}

// TriggerTypeFrom returns the execution's trigger type WithTrigger attached:
// the trigger node's type for a trigger-fired run ("trigger.webhook",
// "trigger.org", …) or the org trigger type a run started from the org side
// was created with. It is set by mono-agent, never by the trigger's payload.
func TriggerTypeFrom(ctx context.Context) string {
	info, _ := ctx.Value(triggerKey{}).(triggerInfo)
	return info.triggerType
}

// CarriesChain reports whether a run with this trigger type has a chain
// trace mono-agent itself wrote into its trigger data (under TraceKey): a
// run the org side started (a granted call, an automation-role message, a
// trigger.org event), or a webhook run, whose WebhookTraceKey field the
// webhook server always sets itself, from a verified X-Monoagent-Trace
// header (internal/tracesig), after removing whatever the body said. A
// manual run's input or a schedule's data can hold anything, so those start
// a fresh chain.
func CarriesChain(triggerType string) bool {
	switch triggerType {
	// A trigger.org run is stored with its node type; org_event only ever
	// appears inside trigger data, so it is not an execution trigger type.
	case TriggerTypeOrgTool, TriggerTypeOrgMessage, TriggerNodeTypeOrg, TriggerNodeTypeWebhook:
		return true
	}
	return false
}

// WebhookTraceKey is where a webhook run's trigger data holds its chain.
// Not `trace`: a webhook body is someone else's payload and may well have
// a field of that name, which must reach the workflow untouched.
const WebhookTraceKey = "monoagent_trace"

// TraceKey is the trigger-data field that holds a run's chain for its
// trigger type.
func TraceKey(triggerType string) string {
	if triggerType == TriggerNodeTypeWebhook {
		return WebhookTraceKey
	}
	return "trace"
}

// ParseTrace reads a `trace` object ({chain_id, hop}) from m, as items and
// org-started trigger data carry it.
func ParseTrace(m map[string]interface{}) (chainID string, hop int, ok bool) {
	return ParseTraceAt(m, "trace")
}

// ParseTraceAt reads a {chain_id, hop} object from m[key]. The chain id
// must look like one and the hop be a non-negative whole number.
func ParseTraceAt(m map[string]interface{}, key string) (chainID string, hop int, ok bool) {
	t, isMap := m[key].(map[string]interface{})
	if !isMap {
		return "", 0, false
	}
	chain, _ := t["chain_id"].(string)
	switch h := t["hop"].(type) {
	case float64:
		if h != float64(int(h)) || h < 0 || h > 1<<30 {
			return "", 0, false
		}
		hop = int(h)
	case int:
		hop = h
	default:
		return "", 0, false
	}
	if !strings.HasPrefix(chain, "chn_") || hop < 0 {
		return "", 0, false
	}
	return chain, hop, true
}

// RunTrace is the chain the running execution belongs to, from its trigger
// data, when its trigger type carries one (CarriesChain). Nodes that send
// work out of mono-agent (the HTTP request node) pass it on signed.
//
// The hop is the deeper of the trigger's and the one item (the node's
// input item, may be nil) reached on the same chain: a run that crossed
// again after it started (its own org.send, whose output item carries the
// new hop) is that far down the chain, and signing the start hop would let
// a loop through it climb one hop per round instead of two. A trace on the
// item naming another chain is ignored, since items can hold outside data
// (an HTTP response lifted to the top level) and that would let the
// outside pick the chain.
func RunTrace(ctx context.Context, item map[string]interface{}) (chainID string, hop int, ok bool) {
	tt := TriggerTypeFrom(ctx)
	if !CarriesChain(tt) {
		return "", 0, false
	}
	chainID, hop, ok = ParseTraceAt(TriggerDataFrom(ctx), TraceKey(tt))
	if !ok {
		return "", 0, false
	}
	if c, h, itemOK := ParseTrace(item); itemOK && c == chainID && h > hop {
		hop = h
	}
	return chainID, hop, true
}

// TriggerNodeTypeWebhook is the webhook trigger's node type, the trigger
// type of a run it fires.
const TriggerNodeTypeWebhook = "trigger.webhook"

// TriggerNodeTypeOrg is the trigger.org node type (orgbridge.TriggerNodeType,
// which this package cannot import), the trigger type of a run it fires.
const TriggerNodeTypeOrg = "trigger.org"

// withoutTrace returns a copy of data without its field key.
func withoutTrace(data map[string]interface{}, key string) map[string]interface{} {
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		if k != key {
			out[k] = v
		}
	}
	return out
}

// snapshotTrigger copies trigger data for the run's context, including the
// chain objects, so a node that edits its input item in place (the trigger
// node's output item shares the map) cannot rewrite the chain the run signs.
func snapshotTrigger(data map[string]interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	out := make(map[string]interface{}, len(data))
	for k, v := range data {
		if m, ok := v.(map[string]interface{}); ok && (k == "trace" || k == WebhookTraceKey || k == "org" || k == "event") {
			cp := make(map[string]interface{}, len(m))
			for mk, mv := range m {
				cp[mk] = mv
			}
			v = cp
		}
		out[k] = v
	}
	return out
}
