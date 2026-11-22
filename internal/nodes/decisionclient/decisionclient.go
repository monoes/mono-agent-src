// Package decisionclient builds the decision client of a workflow node that
// names an optional `provider` (docs/mastermind/plans/2026-11-21-decision-providers.md,
// WS4): ai.choose, ai.decide, browser.jev, the HIL node's auto_decide and
// applications.evaluate's runtime jev. It also holds the one D7 rule every
// such node gates by.
package decisionclient

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/secrets"
	"github.com/monoes/mono-agent/internal/workflow"
)

// Config is what a node's own config says about its decision client.
type Config struct {
	// Provider is the node's `provider` ("" = the surface's, else the machine default).
	Provider string
	// APIKey is the node's `api_key`. It goes only to TypeSafe: for any other
	// provider it is dropped (that provider's key is its machine-vault entry,
	// and the schemas default api_key to @secret:typesafe).
	APIKey string
	// Model is the node's `model`. For a provider other than TypeSafe it is
	// passed only when the node also names that provider itself (Provider),
	// and never when it is a Jev model name (jev-…): a provider the node
	// inherited (the surface's, the default) answers with its record's
	// model, so a model field an agent wrote cannot pick the vendor behind a
	// router the user consented to.
	Model string
}

// ErrRemoteFromAIRun is wrapped by the refusal to send a read-only
// decision node's data to a provider on another host from a run an AI
// started or wrote.
var ErrRemoteFromAIRun = errors.New("a decision provider on another host is not used from a run an AI started or wrote")

// readOnlySurfaces are the decision nodes internal/orggrant classifies as
// read-only (ai.choose, ai.decide, the Human-in-Loop node's auto_decide): a
// grant to a workflow with only these needs no approval, so they may not
// send data off this machine to a host other than TypeSafe's in such a run.
var readOnlySurfaces = map[jevconf.Surface]bool{
	jevconf.NodeSurface("ai.choose"): true, jevconf.NodeSurface("ai.decide"): true, jevconf.HIL: true,
}

// aiRun says why ctx is a run an AI started or wrote ("" when it is not): an
// org's granted call, message or event, a workflow the user never approved,
// an unapproved step, or a step under agent access.
func aiRun(ctx context.Context) string {
	switch workflow.TriggerTypeFrom(ctx) {
	case workflow.TriggerTypeOrgTool, workflow.TriggerTypeOrgMessage, workflow.TriggerNodeTypeOrg, workflow.TriggerTypeOrgEvent:
		return "an org started this run"
	}
	switch {
	case workflow.AIAuthoredFrom(ctx):
		return "the workflow is one you have not approved"
	case secrets.ResolveRefusal(ctx) != "":
		return "an AI wrote this step"
	case secrets.UnderAgentAccess(ctx):
		return "the step runs under agent access"
	}
	return ""
}

// New returns the client a node with cfg uses for profileID on surface s,
// and the provider it resolved. With no provider configured anywhere this is
// exactly jevconf.NewClient(ctx, db, profileID, cfg.APIKey, cfg.Model, s).
func New(ctx context.Context, db *sql.DB, profileID string, cfg Config, s jevconf.Surface) (*jev.Client, string, error) {
	provider := strings.TrimSpace(cfg.Provider)
	explicit := provider != ""
	if !explicit {
		provider = jevconf.ActiveProvider(db, profileID, s)
	}
	if provider == jev.ProviderTypeSafe {
		var c *jev.Client
		var err error
		if explicit {
			c, err = jevconf.NewClientFor(ctx, db, profileID, provider, cfg.APIKey, cfg.Model, s)
		} else {
			// Today's call: it resolves the provider itself and fails on a
			// settings read that fails rather than fall back to TypeSafe.
			c, err = jevconf.NewClient(ctx, db, profileID, cfg.APIKey, cfg.Model, s)
		}
		return c, provider, err
	}
	if readOnlySurfaces[s] {
		if why := aiRun(ctx); why != "" {
			if p, ok, err := jevconf.FindProvider(db, provider); err == nil && ok && !jevconf.Loopback(p.BaseURL) {
				return nil, provider, fmt.Errorf("%w (%s): provider %q is at %s; name a provider on this machine or typesafe, or run the workflow yourself",
					ErrRemoteFromAIRun, why, p.Name, p.BaseURL)
			}
		}
	}
	model := strings.TrimSpace(cfg.Model)
	if !explicit || strings.HasPrefix(model, "jev-") {
		model = ""
	}
	c, err := jevconf.NewClientFor(ctx, db, profileID, provider, "", model, s)
	return c, provider, err
}

// Gate is the value a node compares with its threshold for an answer whose
// top probability is p (D7): 0 when the provider gives no probability, so
// such an answer never passes a threshold above 0. It is jev.GateProbability,
// the rule jev.GateValue applies too.
func Gate(resp *jev.Response, p float64) float64 { return jev.GateProbability(resp, p) }
