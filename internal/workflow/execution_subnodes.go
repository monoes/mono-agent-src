package workflow

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/vault"
)

// maxSubNodeDepth bounds sub-nodes of sub-nodes (a retriever's vector store's
// embedding is three levels down).
const maxSubNodeDepth = 8

// resolveSubNodes resolves the config of the AI sub-nodes attached to rootID
// (and theirs) with the root's expression context, as the root's own config
// is resolved: credentials injected, templates, vault and secret references.
// A disabled sub-node is left out. Fields the sub-node's type holds back as
// per-item (PerItemConfigResolver, e.g. a memory's session key) are left
// raw for the root to resolve against each item.
func (r *runner) resolveSubNodes(rootID string, exprCtx ExpressionContext, depth int) ([]SubNode, error) {
	edges := r.dag.SubNodes(rootID)
	if len(edges) == 0 {
		return nil, nil
	}
	if depth >= maxSubNodeDepth {
		return nil, fmt.Errorf("sub-nodes nested more than %d deep under %q", maxSubNodeDepth, rootID)
	}
	out := make([]SubNode, 0, len(edges))
	for _, e := range edges {
		n := e.Node
		if n.Disabled {
			continue
		}
		if err := n.ParseConfig(); err != nil {
			return nil, fmt.Errorf("sub-node %s (%s): parse config: %w", n.ID, n.Name, err)
		}
		cfg, _ := cloneConfigValue(n.Config).(map[string]interface{})
		if cfg == nil {
			cfg = map[string]interface{}{}
		}
		if e.Kind == "ai_tool" {
			// A tool's config reads the model's arguments: it is resolved
			// when the root calls it, not now.
			node := n
			out = append(out, SubNode{ID: n.ID, Name: n.Name, Type: n.Type, Kind: e.Kind, Config: cfg,
				Call: func(ctx context.Context, args map[string]interface{}) ([]Item, error) {
					return r.callTool(ctx, node, exprCtx, args)
				}})
			continue
		}
		r.injectCredential(n, cfg)
		var perItem []string
		var executor NodeExecutor
		if factory, ok := r.registry.Get(n.Type); ok {
			executor = factory()
			if res, ok := executor.(PerItemConfigResolver); ok {
				perItem = res.PerItemConfigFields()
			}
		}
		stored, _ := cloneConfigValue(cfg).(map[string]interface{})
		held := extractPerItemFields(cfg, perItem)
		resolved, err := r.expr.ResolveConfig(cfg, exprCtx)
		if err != nil {
			return nil, fmt.Errorf("sub-node %s (%s): resolve config: %w", n.ID, n.Name, err)
		}
		restorePerItemFields(resolved, held)
		if err := checkStoredFields(executor, stored, resolved); err != nil {
			return nil, fmt.Errorf("sub-node %s (%s): %w", n.ID, n.Name, err)
		}
		if db := vault.DBFromContext(r.ctx); db != nil {
			profile, perr := vault.ProfileIDFromContext(r.ctx)
			if perr != nil {
				return nil, fmt.Errorf("sub-node %s (%s) ran without a profile: %w", n.ID, n.Name, perr)
			}
			if err := vault.ResolveConfig(r.ctx, db, stored, resolved); err != nil {
				return nil, fmt.Errorf("sub-node %s (%s): %w", n.ID, n.Name, err)
			}
			if err := secrets.ResolveConfig(r.ctx, db, profile, stored, resolved); err != nil {
				return nil, fmt.Errorf("sub-node %s (%s): %w", n.ID, n.Name, err)
			}
		}
		children, err := r.resolveSubNodes(n.ID, exprCtx, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, SubNode{ID: n.ID, Name: n.Name, Type: n.Type, Kind: e.Kind, Config: resolved, SubNodes: children})
	}
	return out, nil
}

// callTool runs the ai_tool sub-node n once with args as its only input item:
// its config is resolved against args (credentials, templates, vault and
// secret references, as for any node; the type's per-item fields are left to
// it), and its main output items are returned.
func (r *runner) callTool(ctx context.Context, n WorkflowNode, base ExpressionContext, args map[string]interface{}) ([]Item, error) {
	factory, ok := r.registry.Get(n.Type)
	if !ok {
		return nil, fmt.Errorf("tool %q: unknown node type %q", n.Name, n.Type)
	}
	executor := factory()
	cfg, _ := cloneConfigValue(n.Config).(map[string]interface{})
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	r.injectCredential(n, cfg)
	var perItem []string
	if res, ok := executor.(PerItemConfigResolver); ok {
		perItem = res.PerItemConfigFields()
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	exprCtx := base
	exprCtx.JSON = args
	stored, _ := cloneConfigValue(cfg).(map[string]interface{})
	held := extractPerItemFields(cfg, perItem)
	resolved, err := r.expr.ResolveConfig(cfg, exprCtx)
	if err != nil {
		return nil, fmt.Errorf("tool %q: resolve config: %w", n.Name, err)
	}
	restorePerItemFields(resolved, held)
	if err := checkStoredFields(executor, stored, resolved); err != nil {
		return nil, fmt.Errorf("tool %q: %w", n.Name, err)
	}
	if db := vault.DBFromContext(ctx); db != nil {
		profile, perr := vault.ProfileIDFromContext(ctx)
		if perr != nil {
			return nil, fmt.Errorf("tool %q ran without a profile: %w", n.Name, perr)
		}
		if err := vault.ResolveConfig(ctx, db, stored, resolved); err != nil {
			return nil, fmt.Errorf("tool %q: %w", n.Name, err)
		}
		if err := secrets.ResolveConfig(ctx, db, profile, stored, resolved); err != nil {
			return nil, fmt.Errorf("tool %q: %w", n.Name, err)
		}
	}
	outs, err := executor.Execute(ctx, NodeInput{
		Items: []Item{NewItem(args)}, NodeOutputs: r.nodeOutputs,
		WorkflowID: r.wf.ID, ExecutionID: r.exec.ID, NodeID: n.ID, NodeName: n.Name, Location: base.Location,
	}, resolved)
	if err != nil {
		return nil, fmt.Errorf("tool %q: %w", n.Name, err)
	}
	var items []Item
	for _, o := range outs {
		if o.Handle == "" || o.Handle == "main" {
			items = append(items, o.Items...)
		}
	}
	return items, nil
}
