// internal/nodes/applications/tag.go
package applicationsnodes

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

// TagNode adds or removes a tag from one application.
// Type: "applications.tag"
type TagNode struct{}

func (n *TagNode) Type() string { return "applications.tag" }

// PublishesToThirdParty: applications.tag sends nothing off this machine (issue #1418).
func (TagNode) PublishesToThirdParty() bool { return false }

func (n *TagNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalStore == nil {
		return nil, fmt.Errorf("applications.tag: store not available (call SetGlobalStore at startup)")
	}
	id := configString(config, "id", "")
	tag := configString(config, "tag", "")
	if id == "" || tag == "" {
		return nil, fmt.Errorf("applications.tag: config \"id\" and \"tag\" are required")
	}
	action := configString(config, "action", "add")
	profileID, err := nodeprofile.Require(ctx, "applications.tag", config)
	if err != nil {
		return nil, err
	}

	switch action {
	case "add":
		err = globalStore.AddTag(ctx, profileID, id, tag)
	case "remove":
		err = globalStore.RemoveTag(ctx, profileID, id, tag)
	default:
		return nil, fmt.Errorf("applications.tag: config \"action\" must be \"add\" or \"remove\", got %q", action)
	}
	if err != nil {
		return nil, fmt.Errorf("applications.tag: %w", err)
	}

	out := map[string]interface{}{"id": id, "tag": tag, "action": action}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}
