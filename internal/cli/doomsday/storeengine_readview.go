package doomsday

import (
	"context"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

// ViewReader is an Engine that can give the control room its read-only snapshot of the profile's latest run. An
// Engine without it (Off, a build without the mode) is the mode off. store.ErrNoRun means the profile has no run.
type ViewReader interface {
	ReadView(ctx context.Context, q store.ViewQuery) (store.View, error)
}

// ReadView reads the profile's latest run through the store's read view; a store without one is the mode off.
func (e *StoreEngine) ReadView(ctx context.Context, q store.ViewQuery) (store.View, error) {
	rv, ok := e.Store.(store.ReadViewer)
	if !ok {
		return store.View{}, ErrOff
	}
	return rv.ReadView(ctx, e.Profile, 0, q)
}
