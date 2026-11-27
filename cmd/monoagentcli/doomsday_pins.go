package main

import (
	"context"
	"slices"
	"time"

	"github.com/monoes/mono-agent/internal/agentroster"
	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
	"github.com/monoes/mono-agent/internal/monomind"
)

// doomsdayPinChoices is the models `doomsday run` may pin, as the app's start form lists them: the validated roster's installed
// runtimes, a runtime being the pin's provider and its version the pin's version, failed models left out, ready ones first and the
// strongest of them before the rest.
func doomsdayPinChoices(ctx context.Context, cfg *globalConfig) ([]doomsdaycli.Pin, error) {
	db, err := initDB(cfg)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	results, err := agentroster.List(ctx, db.DB)
	if err != nil {
		return nil, err
	}
	scan, _ := monomind.Scan(ctx) // a failed scan leaves the versions out, not the roster
	var ready, rest []doomsdaycli.Pin
	for _, rr := range agentroster.Build(results, scan, time.Now(), agentroster.DefaultMaxAge) {
		if !rr.Installed {
			continue
		}
		version := rr.Version
		if version == "" {
			version = "unknown"
		}
		for _, m := range rr.Models {
			if m.Model == "" || m.State == agentroster.StateFailed {
				continue
			}
			p := doomsdaycli.Pin{Provider: rr.Runtime, Model: m.Model, Version: version}
			if m.State == agentroster.StateReady {
				ready = append(ready, p)
			} else {
				rest = append(rest, p)
			}
		}
	}
	// The first choice becomes the boss, so the strongest leads each group.
	for _, group := range [][]doomsdaycli.Pin{ready, rest} {
		slices.SortStableFunc(group, func(a, b doomsdaycli.Pin) int { return monomind.LeadRank(a.Model) - monomind.LeadRank(b.Model) })
	}
	return append(ready, rest...), nil
}
