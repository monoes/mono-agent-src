// internal/nodes/applications/list.go
package applicationsnodes

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

// ListNode fans out one output item per matching application.
// Type: "applications.list"
type ListNode struct{}

func (n *ListNode) Type() string { return "applications.list" }

// PublishesToThirdParty: applications.list sends nothing off this machine (issue #1418).
func (ListNode) PublishesToThirdParty() bool { return false }

func (n *ListNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalStore == nil {
		return nil, fmt.Errorf("applications.list: store not available (call SetGlobalStore at startup)")
	}
	profileID, err := nodeprofile.Require(ctx, "applications.list", config)
	if err != nil {
		return nil, err
	}
	filter := applications.ListFilter{
		Kind:   applications.Kind(configString(config, "kind", "")),
		Status: applications.Status(configString(config, "status", "")),
		Tag:    configString(config, "tag", ""),
	}
	apps, err := globalStore.List(ctx, profileID, filter)
	if err != nil {
		return nil, fmt.Errorf("applications.list: %w", err)
	}

	items := make([]workflow.Item, 0, len(apps))
	for _, a := range apps {
		out := map[string]interface{}{
			"id": a.ID, "kind": string(a.Kind), "status": string(a.Status),
			"tags": a.Tags, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt,
		}
		if a.Job != nil {
			out["company"] = a.Job.Company
			out["url"] = a.Job.URL
		}
		if a.Tender != nil {
			out["issuing_org"] = a.Tender.IssuingOrg
			out["url"] = a.Tender.URL
		}
		items = append(items, workflow.NewItem(out))
	}
	return []workflow.NodeOutput{{Handle: "main", Items: items}}, nil
}
