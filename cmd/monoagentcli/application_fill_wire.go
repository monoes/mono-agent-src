package main

import (
	"context"

	"github.com/monoes/mono-agent/internal/applyfill"
	"github.com/monoes/mono-agent/internal/applypark"
	"github.com/monoes/mono-agent/internal/cli/application"
)

// `application fill start` submits its task as the user's own `aitask submit`; this hands it the
// operator check and the --db-path of this process. The tab a finished fill left open is checked
// against `browser tabs` (run as this process, the operator) before it is parked.
func wireApplicationFillStart(cfg *globalConfig) {
	application.FillStartWiring.RequireOperator = cfg.requireOperator
	application.FillStartWiring.DBPath = func() string { return cfg.DBPath }
	applyfill.Parking = &applyfill.ParkingConfig{
		Live: func(ctx context.Context, profile string) ([]applypark.LiveTab, error) {
			return application.LiveTabs(ctx, profile)
		},
	}
}
