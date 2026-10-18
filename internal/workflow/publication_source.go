package workflow

import (
	"context"
	"github.com/monoes/mono-agent/internal/publication"
	"strings"
)

// PublicationSource uses engine-owned org provenance, never arbitrary node
// config or input items, to attribute a successful publication.
func PublicationSource(ctx context.Context, input NodeInput) publication.Source {
	source := publication.Source{WorkflowID: input.WorkflowID, ExecutionID: input.ExecutionID, NodeID: input.NodeID}
	switch TriggerTypeFrom(ctx) {
	case TriggerTypeOrgTool, TriggerTypeOrgMessage, TriggerNodeTypeOrg, TriggerTypeOrgEvent:
		org, _ := TriggerDataFrom(ctx)["org"].(map[string]interface{})
		source.OrgID, _ = org["name"].(string)
		source.RoleID, _ = org["role"].(string)
		source.AgentID, _ = org["agent_id"].(string)
		// Event subscriptions use a string org and an engine-built event
		// envelope rather than the grant/endpoint org object.
		if name, ok := TriggerDataFrom(ctx)["org"].(string); ok {
			source.OrgID = name
			event, _ := TriggerDataFrom(ctx)["event"].(map[string]interface{})
			actor, _ := event["from"].(string)
			if actor != "" && !strings.HasPrefix(actor, "human:") {
				source.AgentID = actor
				source.RoleID = strings.TrimPrefix(actor, name+":")
			}
		}
	}
	return source
}
