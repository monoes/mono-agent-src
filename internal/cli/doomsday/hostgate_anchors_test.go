package doomsday

import (
	"context"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/coderhost"
	"github.com/monoes/mono-agent/internal/doomsday/stop"
)

// The host starts the process-group service (macOS) when its gate is installed, on the run directory it was given; a gate
// built without it starts nothing and so leaves the run directory alone.
func TestInstallStartsTheAnchorServiceOnlyWhenWired(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	h := &coderhost.Host{Store: w.host, L: &blockLauncher{}}
	(&HostGate{Store: w.w.Chained(), Profile: "p1", RunDir: w.w.RunDir}).Install(w.host, h)
	if n := dirEntries(t, w.w.RunDir.Dir); n != 0 {
		t.Fatalf("a gate without the service touched the run directory (%d entries)", n)
	}
	var got stop.RunDir
	calls := 0
	h2 := &coderhost.Host{Store: w.host, L: &blockLauncher{}}
	(&HostGate{Store: w.w.Chained(), Profile: "p1", RunDir: w.w.RunDir,
		Anchors: func(_ context.Context, d stop.RunDir) error { calls++; got = d; return nil }}).Install(w.host, h2)
	if calls != 1 || got.Dir != w.w.RunDir.Dir {
		t.Fatalf("calls %d dir %+v", calls, got)
	}
}

// The unit is made after the host is ensured, and a host that cannot be started means no unit (so no run).
func TestCreateUnitEnsuresTheHostFirst(t *testing.T) {
	w, _ := newWire(t, func() error { return nil }, fakeKernel{}, nil)
	ensured := 0
	w.w.EnsureHost = func(context.Context) error { ensured++; return errors.New("no executable") }
	_, err := w.w.createUnit(context.Background(), 5)
	if ensured != 1 || !errors.Is(err, stop.ErrUnitNotCreated) {
		t.Fatalf("ensured %d err %v", ensured, err)
	}
	if _, ok, _ := w.w.RunDir.ReadUnitFile(5); ok {
		t.Fatal("a unit was made without its host")
	}
}
