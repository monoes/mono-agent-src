package publicationnodes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

type RegisterNode struct{}

func (*RegisterNode) Type() string { return "publication.register" }
func (*RegisterNode) PerItemConfigFields() []string {
	fields := []string{}
	for key := range publication.EntrySchema() {
		fields = append(fields, key)
	}
	return fields
}

// Execute registers reported publications after a custom publisher has succeeded.
// Only known publication fields are copied, so upstream credentials stay upstream.
func (*RegisterNode) Execute(ctx context.Context, in workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	db := vault.DBFromContext(ctx)
	if db == nil {
		return nil, fmt.Errorf("publication.register: no database in context")
	}
	store := publication.NewStore(db, vault.ProfileIDFromContext(ctx))
	items := in.Items
	if len(items) == 0 {
		items = []workflow.Item{workflow.NewItem(nil)}
	}
	out := []workflow.Item{}
	for i, item := range items {
		if failed, ok := item.JSON["success"].(bool); ok && !failed {
			continue
		}
		if v := item.JSON["error"]; v != nil && v != "" {
			continue
		}
		fields := map[string]interface{}{}
		expressions := workflow.NewExpressionEngine()
		exprCtx := workflow.ExpressionContext{JSON: item.JSON, Node: in.NodeOutputs, WorkflowID: in.WorkflowID, ExecutionID: in.ExecutionID}
		for key := range publication.EntrySchema() {
			value, exists := item.JSON[key]
			fromConfig := false
			if configured, ok := config[key]; ok && configured != nil && configured != "" {
				value, exists = configured, true
				fromConfig = true
			}
			if !exists {
				continue
			}
			if text, ok := value.(string); ok {
				resolved := text
				if fromConfig {
					var err error
					resolved, err = expressions.EvaluateString(text, exprCtx)
					if err != nil {
						return nil, fmt.Errorf("publication.register item %d field %s: %w", i, key, err)
					}
				}
				value = resolved
				if key == "media" {
					var media []string
					if err := json.Unmarshal([]byte(resolved), &media); err != nil {
						return nil, fmt.Errorf("publication.register: media must be a JSON array of strings: %w", err)
					}
					value = media
				}
			}
			fields[key] = value
		}
		b, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		var entry publication.Entry
		if err = json.Unmarshal(b, &entry); err != nil {
			return nil, fmt.Errorf("publication.register: %w", err)
		}
		source := workflow.PublicationSource(ctx, in)
		entry.WorkflowID, entry.ExecutionID, entry.NodeID = source.WorkflowID, source.ExecutionID, source.NodeID
		if source.OrgID != "" {
			entry.OrgID = source.OrgID
		}
		if source.RoleID != "" {
			entry.RoleID = source.RoleID
		}
		if source.AgentID != "" {
			entry.AgentID = source.AgentID
		}
		if entry.IdempotencyKey == "" && in.ExecutionID != "" {
			entry.IdempotencyKey = fmt.Sprintf("registration:%s:%s:%d", in.ExecutionID, in.NodeID, i)
		}
		registered, err := store.Register(ctx, entry)
		if err != nil {
			return nil, fmt.Errorf("publication.register item %d: %w", i, err)
		}
		encoded, _ := json.Marshal(registered)
		var data map[string]interface{}
		_ = json.Unmarshal(encoded, &data)
		out = append(out, workflow.NewItem(data))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: out}}, nil
}

func RegisterAll(r *workflow.NodeTypeRegistry) {
	r.Register("publication.register", func() workflow.NodeExecutor { return &RegisterNode{} })
}
