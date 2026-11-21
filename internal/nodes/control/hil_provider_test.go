package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/monoes/mono-agent/internal/hilsuggest"
	"github.com/monoes/mono-agent/internal/jev"
	"github.com/monoes/mono-agent/internal/jev/jevconf"
	"github.com/monoes/mono-agent/internal/jev/jevtest"
	"github.com/monoes/mono-agent/internal/workflow"
)

// hilProvider saves provider "local", a fake that approves everything.
func hilProvider(t *testing.T, db *sql.DB, asDefault bool) *jevtest.FakeBackend {
	t.Helper()
	fb := jevtest.NewFakeBackend(jevtest.Fixed(map[string]string{hilsuggest.QDecision: hilsuggest.Approve}))
	t.Cleanup(jevconf.RegisterKind("fake-hil", func(jevconf.Provider, string) (jev.Backend, error) { return fb, nil }))
	if err := jevconf.SaveProvider(context.Background(), db, jevconf.Provider{
		Name: "local", Kind: "fake-hil", BaseURL: "http://localhost", Model: "fake-model",
	}); err != nil {
		t.Fatal(err)
	}
	if asDefault {
		if err := jevconf.SetDefaultProvider(context.Background(), db, "local"); err != nil {
			t.Fatal(err)
		}
	}
	return fb
}

func TestHILAuto_DefaultProviderDecides(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, map[string]string{"alpha": hilsuggest.Reject})
	enable(t, db)
	fb := hilProvider(t, db, true)
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"policy": "ok"})); err != nil {
		t.Fatalf("err = %v, want every item approved by the provider", err)
	}
	if fb.Calls() != 3 || srv.Calls() != 0 {
		t.Fatalf("fake %d, TypeSafe %d calls", fb.Calls(), srv.Calls())
	}
	for _, r := range rowsOf(t, db) {
		c := cfgOf(t, r)
		if r.Status != "approved" || c["decided_by"] != "jev:fake-model" || c["provider"] != "local" || c["probability_kind"] != "logprob" {
			t.Fatalf("row = %+v", r)
		}
	}
}

func TestHILAuto_ConfigProviderWins(t *testing.T) {
	db := newAutoTestDB(t)
	srv := jevByCaption(t, nil)
	enable(t, db)
	fb := hilProvider(t, db, false)
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"provider": "local"})); err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 3 || srv.Calls() != 0 {
		t.Fatalf("fake %d, TypeSafe %d calls", fb.Calls(), srv.Calls())
	}
	if _, err := (&HumanInLoopNode{}).Execute(autoCtx(newAutoTestDB(t)), threeItems(), autoConfig(map[string]interface{}{"provider": 3})); !errors.Is(err, workflow.ErrInvalidConfig) {
		t.Errorf("a non-string provider: err = %v", err)
	}
}

// D7: a provider without probabilities settles nothing; every item waits
// with its suggestion.
func TestHILAuto_NoProbabilityNeverSettles(t *testing.T) {
	db := newAutoTestDB(t)
	jevByCaption(t, nil)
	enable(t, db)
	fb := hilProvider(t, db, true)
	fb.Caps.Probability = jev.ProbNone
	_, err := (&HumanInLoopNode{}).Execute(autoCtx(db), threeItems(), autoConfig(map[string]interface{}{"policy": "ok"}))
	if !errors.Is(err, workflow.ErrNodePaused) {
		t.Fatalf("err = %v, want paused", err)
	}
	for _, r := range rowsOf(t, db) {
		c := cfgOf(t, r)
		if r.Status != "pending" || c["suggestion"] == nil || c["probability_kind"] != "none" {
			t.Fatalf("row = %+v", r)
		}
	}
}
