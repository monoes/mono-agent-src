package workflow

import (
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
		r.injectCredential(n, cfg)
		var perItem []string
		if factory, ok := r.registry.Get(n.Type); ok {
			if res, ok := factory().(PerItemConfigResolver); ok {
				perItem = res.PerItemConfigFields()
			}
		}
		held := extractPerItemFields(cfg, perItem)
		resolved, err := r.expr.ResolveConfig(cfg, exprCtx)
		if err != nil {
			return nil, fmt.Errorf("sub-node %s (%s): resolve config: %w", n.ID, n.Name, err)
		}
		restorePerItemFields(resolved, held)
		if db := vault.DBFromContext(r.ctx); db != nil {
			_ = vault.ResolveConfig(r.ctx, db, resolved)
			_ = secrets.ResolveConfig(r.ctx, db, vault.ProfileIDFromContext(r.ctx), resolved)
		}
		children, err := r.resolveSubNodes(n.ID, exprCtx, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, SubNode{ID: n.ID, Name: n.Name, Type: n.Type, Kind: e.Kind, Config: resolved, SubNodes: children})
	}
	return out, nil
}
