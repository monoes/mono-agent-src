package doomsday

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
	"github.com/monoes/mono-agent/internal/doomsday/store"
)

type macCounter struct {
	in store.Keyer
	n  atomic.Int32
}

func (k *macCounter) MAC(d string, p ...[]byte) ([]byte, error) { k.n.Add(1); return k.in.MAC(d, p...) }

// Building what a coder host builds (the gate over the chained store, the ops route, the freshness check) and dispatching with
// no run makes no pinned connection, reads no key and warns about nothing: the chain is built when a run needs it.
func TestBuildingTheHostGateAndOpsWithNoRunPinsNothing(t *testing.T) {
	var pins, warns atomic.Int32
	real := PinFunc
	PinFunc = func(context.Context, stop.PinOptions) (*stop.Pinned, error) {
		pins.Add(1)
		return nil, errors.New("counted")
	}
	defer func() { PinFunc = real }()

	r, db := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	keys := &macCounter{in: store.NewHMACKeyer([]byte("k"))}
	w := r.w
	w.RunDir = stop.RunDir{Dir: t.TempDir() + "/fresh-run"} // a pin cache key no earlier test used
	w.Store = store.New(db.DB, keys)
	ws := *w.Witness
	ws.Keys = keys
	ws.Warn = func(string) { warns.Add(1) }
	w.Witness = &ws
	pins.Store(0)
	keys.n.Store(0)

	g := &HostGate{Store: w.Chained(), Fresh: w.Fresh(), Profile: "p1", RunDir: w.RunDir, BindingOn: func() (bool, error) { return true, nil }}
	h := &coderhost.Host{Store: r.host, L: &fakeLauncher{}}
	g.Install(r.host, h)
	w.InstallOps(r.host)
	for i := 0; i < 5; i++ {
		if err := g.Start(coderhost.PointTurnStart, func() (coderhost.StartedProc, error) { return fakeProc{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if pins.Load() != 0 || keys.n.Load() != 0 || warns.Load() != 0 {
		t.Fatalf("with no run: %d pin(s), %d key read(s), %d warning(s)", pins.Load(), keys.n.Load(), warns.Load())
	}
}
