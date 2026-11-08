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
