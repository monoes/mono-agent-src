package publicationnodes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/monoes/mono-agent/internal/hilsend"
	"github.com/monoes/mono-agent/internal/operator"
	"github.com/monoes/mono-agent/internal/publication"
	"github.com/monoes/mono-agent/internal/vault"
	"github.com/monoes/mono-agent/internal/workflow"
)

type RegisterNode struct{}

func (*RegisterNode) Type() string { return "publication.register" }

// PublishesToThirdParty: publication.register sends nothing off this machine (issue #1418).
func (*RegisterNode) PublishesToThirdParty() bool { return false }
func (*RegisterNode) PerItemConfigFields() []string {
	fields := []string{"approval"} // evaluated per item, like the entry's fields
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
	profile, perr := vault.ProfileIDFromContext(ctx)
	if perr != nil {
		return nil, fmt.Errorf("publication.register: ran without a profile: %w", perr)
	}
	store := publication.NewStore(db, profile)
	// A run an AI started is not the operator's: what it registers belongs to
	// a send the user approved and that was sent, named by approval, as
	// `publication register` asks of an agent.
	// A command that runs this node (`node run`) says when its process is an agent's although it
	// carries no marker (workflow.WithAgentCaller); the engine sets nothing.
	callerWhy := workflow.AgentCallerFrom(ctx)
	aiRun := operator.AIRun(ctx) || callerWhy != ""
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
		approval := ""
		if aiRun {
			text, _ := config["approval"].(string)
			var err error
			if approval, err = expressions.EvaluateString(text, exprCtx); err != nil {
				return nil, fmt.Errorf("publication.register item %d field approval: %w", i, err)
			}
		}
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
		var claim *hilsend.PublicationClaim
		if aiRun {
			if claim, err = hilsend.ClaimPublicationApproval(ctx, db, profile, approval, entry); err != nil {
				why := operator.Refusal()
				if why == "" {
					why = callerWhy
				}
				if why != "" {
					// The engine itself is marked, or the command that ran this was an agent's: say so, and how to fix it.
					return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id). This run is not the user's: %s", i, err, why)
				}
				if cerr := workflow.ApprovalCheckError(ctx); cerr != nil {
					return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id). This run counts as an AI's because "+
						"whether you approved this workflow could not be checked (%v): run it again", i, err, cerr)
				}
				if workflow.AIAuthoredFrom(ctx) {
					// The definition is one the user never approved: say how to.
					return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id). This run counts as an AI's because "+
						"this workflow changed since you last saved or activated it: if the change is yours, run `monoagentcli --profile %s workflow activate %s` "+
						"(or save it in the app)", i, err, profile, in.WorkflowID)
				}
				data := workflow.TriggerDataFrom(ctx)
				if caller, ok := workflow.CalledByUnapproved(data); ok && data[workflow.AICallerKey] == workflow.UnapprovedWorkflowCaller {
					// A workflow the user never approved called this one, and
					// nothing else makes this run an AI's: say which, and how
					// to approve it.
					if caller == "" {
						return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id). This run counts as an AI's because "+
							"the workflow that called this one changed since you last saved or activated it: if the change is yours, save or activate that workflow", i, err)
					}
					return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id). This run counts as an AI's because "+
						"the workflow that called this one, %s, changed since you last saved or activated it: if the change is yours, run `monoagentcli --profile %s workflow activate %s` "+
						"(or save it in the app)", i, err, caller, profile, caller)
				}
				return nil, fmt.Errorf("publication.register item %d: %w (set approval to the send's id)", i, err)
			}
		}
		registered, err := store.Register(ctx, entry)
		if err != nil {
			claim.Release(ctx, db)
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
