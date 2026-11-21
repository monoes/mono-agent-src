// Package decisionclient builds the decision client of a workflow node that
// names an optional `provider` (docs/mastermind/plans/2026-11-21-decision-providers.md,
// WS4): ai.choose, ai.decide, browser.jev, the HIL node's auto_decide and
// applications.evaluate's runtime jev. It also holds the one D7 rule every
// such node gates by.
package decisionclient

import (
	"context"
	"database/sql"
	"strings"

	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
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
	// passed only when it is not the Jev default (a Jev model name means
	// nothing to another provider); otherwise the provider's own model applies.
	Model string
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
	model := strings.TrimSpace(cfg.Model)
	if model == jev.DefaultModel {
		model = ""
	}
	c, err := jevconf.NewClientFor(ctx, db, profileID, provider, "", model, s)
	return c, provider, err
}

// Gate is the value a node compares with its threshold for an answer whose
// top probability is p (D7): 0 when the provider gives no probability, so
// such an answer never passes a threshold above 0.
func Gate(resp *jev.Response, p float64) float64 {
	if resp != nil && resp.Probability == jev.ProbNone {
		return 0
	}
	return p
}
