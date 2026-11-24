package main

import (
	"context"
	"errors"

	doomsdaycli "github.com/monoes/mono-agent/internal/cli/doomsday"
)

// firstTurnFor is `status`'s read of the boss's first turn. It opens the database the way the control room's reads do
// (mode=ro, query_only): it creates no file, migrates nothing and writes nothing. A database that is missing or older than the
// mode has no run, so no first turn.
func firstTurnFor(cfg *globalConfig) func(ctx context.Context) (doomsdaycli.FirstTurnState, error) {
	return func(ctx context.Context) (doomsdaycli.FirstTurnState, error) {
		db, err := openReadDB(cfg)
		if errors.Is(err, errNoReadableSchema) {
			return doomsdaycli.FirstTurnState{}, nil
		}
		if err != nil {
			return doomsdaycli.FirstTurnState{}, err
		}
		defer db.Close()
		return doomsdaycli.Wiring{DB: db, Profile: cfg.ProfileID}.FirstTurnOf(ctx)
	}
}

// modelSpendFor is the read of the open run's model-turn spend for `status` and `overview`: informational, from the same
// read-only database handle as firstTurnFor (it creates, migrates and writes nothing; a missing or older database has no run).
func modelSpendFor(cfg *globalConfig) func(ctx context.Context) (*doomsdaycli.ModelSpend, error) {
	return func(ctx context.Context) (*doomsdaycli.ModelSpend, error) {
		db, err := openReadDB(cfg)
		if errors.Is(err, errNoReadableSchema) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		defer db.Close()
		return doomsdaycli.Wiring{DB: db, Profile: cfg.ProfileID}.ModelSpendOf(ctx)
	}
}
