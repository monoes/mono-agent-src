package workflow

import (
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/commsync"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// PerItemExecutionSetting is the Workflow.Settings key that makes app,
// database and queue nodes run once per input item, n8n's model, instead
// of once for the whole batch with config resolved from the first item.
// Off by default; a node opts in on its own with execute_once_per_item.
const PerItemExecutionSetting = "per_item_execution"

// perItemTypePrefixes are the node types PerItemExecutionSetting covers:
// nodes that act on an outside system once per call. Core, data and AI
// nodes work on the whole batch and keep doing so.
var perItemTypePrefixes = []string{"service.", "comm.", "db.", "queue.", "app."}

// perItemPlan is what a per-item run needs to resolve each item's config.
type perItemPlan struct {
	config  map[string]interface{} // unresolved, credentials injected
	fields  []string               // PerItemConfigResolver fields to hold back
	exprCtx ExpressionContext      // prepared; JSON is swapped per item
}

func configFlag(config map[string]interface{}, key string) (value, set bool) {
	v, ok := config[key].(bool)
	return v, ok
}

// executeOnce reports n8n's "Execute Once" node setting: the node runs
// with only its first input item.
func executeOnce(config map[string]interface{}) bool {
	a, _ := configFlag(config, "execute_once")
	b, _ := configFlag(config, "executeOnce")
	return a || b
}

// runsPerItem reports whether node runs once per input item: the node's
// own execute_once_per_item flag decides (false opts out of the workflow
// setting); otherwise settings.per_item_execution does, for the node types
// it covers that do not already resolve their config per item. Loop nodes
// and Execute Once nodes always see the batch.
func (r *runner) runsPerItem(node WorkflowNode, executor NodeExecutor, config map[string]interface{}, items int) bool {
	if items < 2 || r.dag.isLoopNode(node.ID) || executeOnce(config) {
		return false
	}
	if v, ok := configFlag(config, "execute_once_per_item"); ok {
		return v
	}
	if on, _ := r.wf.Settings[PerItemExecutionSetting].(bool); !on {
		return false
	}
	if _, ok := executor.(PerItemConfigResolver); ok {
		return false
	}
	for _, p := range perItemTypePrefixes {
		if strings.HasPrefix(node.Type, p) {
			return true
		}
	}
	return false
}

// planPerItem returns the per-item plan for a node that runsPerItem, or
// nil. It must run before the config's per-item fields are extracted.
func (r *runner) planPerItem(node WorkflowNode, executor NodeExecutor, config map[string]interface{}, items int, fields []string, exprCtx ExpressionContext) *perItemPlan {
	if !r.runsPerItem(node, executor, config, items) {
		return nil
	}
	cfg, _ := cloneConfigValue(config).(map[string]interface{})
	return &perItemPlan{config: cfg, fields: fields, exprCtx: exprCtx.Prepared()}
}

// cloneConfigValue copies the maps and slices of a config value so that
// resolving it for one item cannot change it for the next.
func cloneConfigValue(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, x := range t {
			out[k] = cloneConfigValue(x)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, x := range t {
			out[i] = cloneConfigValue(x)
		}
		return out
	}
	return v
}

// resolveFor resolves the plan's config against one item, as prepare does
// for the batch against the first item.
func (r *runner) resolveFor(t *nodeTask, item Item) (map[string]interface{}, error) {
	cfg, _ := cloneConfigValue(t.perItem.config).(map[string]interface{})
	state := extractPerItemFields(cfg, t.perItem.fields)
	resolved, err := r.expr.ResolveConfig(cfg, t.perItem.exprCtx.WithJSON(item.JSON))
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	restorePerItemFields(resolved, state)
	if err := checkStoredFields(t.executor, t.perItem.config, resolved); err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	if vaultDB := vault.DBFromContext(t.ctx); vaultDB != nil {
		profile, perr := vault.ProfileIDFromContext(t.ctx)
		if perr != nil {
			return nil, fmt.Errorf("resolve config: ran without a profile: %w", perr)
		}
		if err := vault.ResolveConfig(t.ctx, vaultDB, t.perItem.config, resolved); err != nil {
			return nil, fmt.Errorf("resolve config: %w", err)
		}
		if err := secrets.ResolveConfig(t.ctx, vaultDB, profile, t.perItem.config, resolved); err != nil {
			return nil, fmt.Errorf("resolve config: %w", err)
		}
	}
	return resolved, nil
}

// runTask executes a prepared node with retry. Safe to call concurrently
// for independent tasks: it touches only the task and the store.
func (r *runner) runTask(t *nodeTask) {
	// A reply the node opens is marked when what runs after it may send a
	// message to a person (wait_reply_outbound.go); worked out only then.
	nodeID := t.node.ID
	t.ctx = withRunsAfter(t.ctx, func() commsync.Reachable { return r.reachableFrom(nodeID) })
	ctx := t.ctx
	// Communications: tell a Human-in-Loop node what it is approving, and
	// mark the approved messages that wait for any other node as sending
	// while it runs and as sent or failed once it ends.
	if r.hasHIL {
		db := vault.DBFromContext(ctx)
		if t.node.Type == humanInLoopType {
			t.ctx = commsync.WithDownstream(ctx, r.wf.Name, downstreamOf(r.dag, t.node.ID))
			t.ctx = commsync.WithReachable(t.ctx, r.reachableFrom(t.node.ID))
		} else {
			commsync.NodeStarted(ctx, db, r.exec.ID, t.node.ID)
			defer func() {
				if !errors.Is(t.execErr, ErrNodePaused) {
					commsync.NodeFinished(ctx, db, r.exec.ID, t.node.ID, t.execErr)
				}
			}()
		}
	}
	logger := r.logger
	observe := func(outputs []NodeOutput) {
		t.attempts++
		// Capture returned successful items even when another item failed. Tracking
		// errors never change the publisher's outcome or retry decisions.
		if db := vault.DBFromContext(ctx); db != nil {
			pubOutputs := make([]publication.Output, 0, len(outputs))
			for _, out := range outputs {
				items := make([]map[string]interface{}, 0, len(out.Items))
				for _, item := range out.Items {
					items = append(items, item.JSON)
				}
				pubOutputs = append(pubOutputs, publication.Output{Handle: out.Handle, Items: items})
			}
			entries := publication.Normalize(publication.CaptureInput{NodeType: t.node.Type, Config: t.config, Outputs: pubOutputs,
				Source: withNodeRun(PublicationSource(ctx, t.input), t.execNode.ID)})
			if profile, perr := vault.ProfileIDFromContext(ctx); perr != nil {
				logger.Warn().Err(perr).Msg("publication not recorded: ran without a profile")
			} else {
				publication.Record(ctx, publication.NewStore(db, profile), entries, func(msg string) { logger.Warn().Msg(msg) })
			}
		}
	}
	if t.perItem != nil {
		t.outputs, t.execErr = r.runPerItem(t, observe)
		return
	}
	t.runs = 1
	t.outputs, t.execErr = executeWithRetry(t.ctx, t.executor, t.input, t.config, t.retryPolicy, observe)
}

// runPerItem executes t once per input item and concatenates the outputs
// per handle. A failed item follows its own on_error: continue passes the
// item on, skip drops it, error_branch emits an error item on the "error"
// handle; stop (the default) fails the node. A pause stops the loop.
func (r *runner) runPerItem(t *nodeTask, observe func([]NodeOutput)) ([]NodeOutput, error) {
	var outputs []NodeOutput
	at := map[string]int{}
	add := func(handle string, items []Item) {
		if handle == "" {
			handle = "main"
		}
		i, ok := at[handle]
		if !ok {
			i = len(outputs)
			at[handle] = i
			outputs = append(outputs, NodeOutput{Handle: handle})
		}
		outputs[i].Items = append(outputs[i].Items, items...)
	}
	for i, item := range t.input.Items {
		in := t.input
		in.Items = []Item{item}
		in.ItemKeys = []string{t.input.ItemKey(i)}
		config, err := r.resolveFor(t, item)
		if err == nil && len(t.input.SubNodes) > 0 {
			// Sub-node config follows the item too, not the first one.
			in.SubNodes, err = r.resolveSubNodes(t.node.ID, t.perItem.exprCtx.WithJSON(item.JSON), 0)
		}
		onError := t.onError
		var outs []NodeOutput
		if err == nil {
			onError = extractOnError(config)
			t.runs++
			outs, err = executeWithRetry(t.ctx, t.executor, in, config, extractRetryPolicy(config), observe)
		}
		if err == nil {
			for _, o := range outs {
				add(o.Handle, o.Items)
			}
			continue
		}
		if errors.Is(err, ErrNodePaused) || errors.Is(err, ErrExecutionCancelled) {
			return outputs, err
		}
		switch onError {
		case "continue":
			add("main", in.Items)
		case "skip":
		case "error_branch":
			add("error", []Item{NewItem(map[string]interface{}{
				"error": err.Error(), "node_id": t.node.ID, "node": t.node.Name, "item_index": i,
			})})
		default:
			return nil, fmt.Errorf("item %d: %w", i, err)
		}
		t.itemFailures++
	}
	return outputs, nil
}
