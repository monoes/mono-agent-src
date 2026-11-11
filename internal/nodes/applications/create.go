// internal/nodes/applications/create.go
package applicationsnodes

import (
	"context"
	"fmt"

	"github.com/monoes/mono-agent/internal/applications"
	"github.com/monoes/mono-agent/internal/nodes/nodeprofile"
	"github.com/monoes/mono-agent/internal/workflow"
)

// CreateNode creates one job or tender application per Execute call from
// its config (Phase 1 is config-driven, not per-input-item — see
// docs/mastermind/plans/2026-09-05-applications-foundation.md Task 7).
// Type: "applications.create"
type CreateNode struct{}

func (n *CreateNode) Type() string { return "applications.create" }

// PublishesToThirdParty: applications.create sends nothing off this machine (issue #1418).
func (CreateNode) PublishesToThirdParty() bool { return false }

// WritesLocalStore: applications.create puts what it is given where another run can read it back (issue #1418).
func (CreateNode) WritesLocalStore() bool { return true }

func (n *CreateNode) Execute(ctx context.Context, input workflow.NodeInput, config map[string]interface{}) ([]workflow.NodeOutput, error) {
	if globalStore == nil {
		return nil, fmt.Errorf("applications.create: store not available (call SetGlobalStore at startup)")
	}

	profileID, err := nodeprofile.Require(ctx, "applications.create", config)
	if err != nil {
		return nil, err
	}
	kind := configString(config, "kind", "")
	app := &applications.Application{
		Kind:      applications.Kind(kind),
		ProfileID: profileID,
	}

	switch app.Kind {
	case applications.KindJob:
		app.Job = &applications.JobDetails{
			Title:           configString(config, "title", ""),
			Company:         configString(config, "company", ""),
			URL:             configString(config, "url", ""),
			Location:        configString(config, "location", ""),
			Description:     configString(config, "description", ""),
			CompensationMin: configFloatPtr(config, "compensation_min"),
			CompensationMax: configFloatPtr(config, "compensation_max"),
			Currency:        configString(config, "currency", ""),
			JobType:         configString(config, "job_type", ""),
			IsRemote:        configBoolPtr(config, "is_remote"),
			Source:          configString(config, "source", "manual"),
			PostedAt:        configString(config, "posted_at", ""),
		}
	case applications.KindTender:
		app.Tender = &applications.TenderDetails{
			Title:                  configString(config, "title", ""),
			IssuingOrg:             configString(config, "issuing_org", ""),
			URL:                    configString(config, "url", ""),
			Description:            configString(config, "description", ""),
			SubmissionDeadline:     configString(config, "submission_deadline", ""),
			EstimatedValue:         configFloatPtr(config, "estimated_value"),
			Currency:               configString(config, "currency", ""),
			RequiredCertifications: configString(config, "required_certifications", ""),
			BidDocumentsRequired:   configString(config, "bid_documents_required", ""),
			Source:                 configString(config, "source", "manual"),
			PublishedAt:            configString(config, "published_at", ""),
		}
	default:
		return nil, fmt.Errorf("applications.create: config \"kind\" must be \"job\" or \"tender\", got %q", kind)
	}

	if err := globalStore.Create(ctx, app); err != nil {
		return nil, fmt.Errorf("applications.create: %w", err)
	}

	out := map[string]interface{}{
		"id": app.ID, "kind": string(app.Kind), "status": string(app.Status),
		"profile_id": app.ProfileID, "created_at": app.CreatedAt, "updated_at": app.UpdatedAt,
	}
	return []workflow.NodeOutput{{Handle: "main", Items: []workflow.Item{workflow.NewItem(out)}}}, nil
}
