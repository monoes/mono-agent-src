package doomsday

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/monoes/mono-agent/internal/doomsday/store"
)

func realEngine(t *testing.T) *StoreEngine {
	t.Helper()
	keyring.MockInit()
	db, err := newMigratedDatabase(t.TempDir() + "/dd.db")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ApplyMigrations(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	db.DB.SetMaxOpenConns(1)
	return &StoreEngine{Store: store.New(db.DB, store.NewHMACKeyer([]byte("k"))), Profile: "p1",
		Binding: func(context.Context) (bool, error) { return true, nil },
		CreateUnit: func(context.Context, int64) (func(context.Context) error, error) {
			return func(context.Context) error { return nil }, nil
		}}
}

func TestRealStoreStartStatusAndSecondRunRefused(t *testing.T) {
	file := writeCharter(t, goodCharter)
	e := realEngine(t)
	r := newRig()
	r.deps.Engine = func() Engine { return e }

	// no containment probe: the mode refuses, nothing is written
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "containment") {
		t.Fatalf("started without a containment probe: %v", err)
	}
	if s, _ := e.Status(context.Background()); s.Active {
		t.Fatal("a run exists")
	}

	e.ContainmentProbe = func() error { return nil }
	h := fullHash(t, r, file)
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", strings.Repeat("0", 64)); err == nil {
		t.Fatal("started on a wrong hash")
	}
	if out, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h); err != nil {
		t.Fatal(err, out)
	}
	out, err := run(t, r.deps, "", "status")
	if err != nil || !strings.Contains(out, "running") || !strings.Contains(out, "generation 1") {
		t.Fatalf("status: %v %q", err, out)
	}
	// a second start for the same profile is refused by the store (the generation moved, so the hash differs too)
	h2 := fullHash(t, r, file)
	if _, err := run(t, r.deps, "", "start", "--charter", file, "--yes-i-lift", "--hash", h2); err == nil || !errors.Is(err, store.ErrActiveRun) {
		t.Fatalf("second run not refused: %v", err)
	}
}

func TestRealStoreStopFailsClosedWhenNotWired(t *testing.T) {
	e := realEngine(t)
	r := newRig()
	r.deps.Engine = func() Engine { return e }
	out, err := run(t, r.deps, "", "stop")
	if err == nil || strings.Contains(out, "stopped\n") {
		t.Fatalf("stop claimed success without a stop package: %v %q", err, out)
	}
}

func TestMappingKeepsEveryField(t *testing.T) {
	c, _ := LoadCharter(strings.NewReader(goodCharter))
	c.Tick(EverythingLines()...)
	c.PerAsk = PerAsk{SpendMicro: 5, Tasks: 2, Access: "edit", Depth: 1}
	c.Criteria = []Criterion{{Kind: "command", Command: "go test", Expected: "ok"}, {Kind: "rubric", Text: "nice"}}
	sc := toStore(c, "p", 3)
	if sc.Generation != 3 || len(sc.Lines) != 6 || sc.BossPin.PurposePolicy != "j1" || sc.VerifierPin.Model != "v" ||
		!sc.PerAsk.Declared || sc.Criteria[0].Spec != "go test" || sc.Criteria[1].Spec != "nice" || sc.Limits.BudgetMicro != 5000000 {
		t.Fatalf("%+v", sc)
	}
	for _, l := range sc.Lines {
		if l == store.LinePlatformCaps {
			t.Fatal("everything carried platform caps")
		}
	}
}
