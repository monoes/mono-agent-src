// internal/nodes/applications/set_status.go
package applicationsnodes

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

// SetStatusNode transitions one application's status to "rejected" or
// "cancelled". Actor defaults to "system" since this runs inside an
// automated workflow.
//
// "applied" is deliberately NOT an allowed target here, even though
// applications.Status permits the pending->applied edge at the store
// level: `application send` (a human-invoked CLI command) is meant to be
// the only path that records an application as applied. Letting an
// unattended workflow reach the same state via this node would let a
// scheduled/triggered workflow (e.g. discover -> evaluate -> set_status)
// silently "apply" to jobs with no document generation, no browser
// review, and no human action at all -- exactly the automation this
// feature's apply flow was designed to stop short of. This mirrors
// internal/nodes/apply/prepare.go's doc comment, which deliberately does
// not expose apply.OpenForApplication as a node for the same reason.
type SetStatusNode struct{}

func (n *SetStatusNode) Type() string { return "applications.set_status" }

// PublishesToThirdParty: applications.set_status sends nothing off this machine (issue #1418).
func (SetStatusNode) PublishesToThirdParty() bool { return false }

func (n *SetStatusNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalStore == nil {
		return nil, fmt.Errorf("applications.set_status: store not available (call SetGlobalStore at startup)")
	}
	id := configString(config, "id", "")
	if id == "" {
		return nil, fmt.Errorf("applications.set_status: config \"id\" is required")
	}
	status := configString(config, "status", "")
	if applications.Status(status) == applications.StatusApplied {
		return nil, fmt.Errorf("applications.set_status: \"applied\" is not a valid target here -- use the `application send` CLI command, which requires an explicit human action")
	}
	profileID, err := nodeprofile.Require(ctx, "applications.set_status", config)
	if err != nil {
		return nil, err
	}
	actor := applications.Actor(configString(config, "actor", string(applications.ActorSystem)))
	note := configString(config, "note", "")

	if err := globalStore.SetStatus(ctx, profileID, id, applications.Status(status), actor, note); err != nil {
		return nil, fmt.Errorf("applications.set_status: %w", err)
	}

	out := map[string]interface{}{"id": id, "status": status}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}
