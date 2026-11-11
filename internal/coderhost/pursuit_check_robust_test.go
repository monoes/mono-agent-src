package coderhost

import (
	"context"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"os"
	"path/filepath"
	"testing"
)

// A project with more entries than the manifest cap is never looked at again as a tree that may be quiet: no check starts.
// Revert proof: take the cap out of StatManifestCtx's use and the big project is checked.
func TestAHugeProjectIsNotCheckedAndSaysSo(t *testing.T) {
	old := maxManifestEntries
	maxManifestEntries = 3
	t.Cleanup(func() { maxManifestEntries = old })
	w := newCW(t)
	for _, n := range []string{"b", "c", "d", "e"} {
		if err := os.WriteFile(filepath.Join(w.root, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 10; i++ {
		w.step()
	}
	if w.checkRows() != 0 || !w.h.checkBig[77] {
		t.Fatalf("checks %v, big noted %v", w.checks(), w.h.checkBig)
	}
}

type orderConfiner struct {
	scratchThere func() bool
	atSweep      *bool
}

func (c orderConfiner) Sweep(context.Context) ([]string, error) {
	*c.atSweep = c.scratchThere()
	return nil, nil
}
func (c orderConfiner) SweepEnv(context.Context) ([]string, error) { return nil, nil }
func (c orderConfiner) Cleanup(context.Context) ([]string, error)  { return nil, nil }

// The start ends a dead host's units BEFORE it removes the scratch folders they may still be using.
func TestTheStartEndsUnitsBeforeItRemovesScratch(t *testing.T) {
	w := newCW(t)
	left := filepath.Join(w.dep.Scratch, "check-old")
	if err := os.MkdirAll(left, 0o700); err != nil {
		t.Fatal(err)
	}
	var atSweep bool
	hk := orderConfiner{scratchThere: func() bool { _, err := os.Stat(left); return err == nil }, atSweep: &atSweep}
	w.dep.Confiner = &housekeepingConfiner{orderConfiner: hk}
	w.h.pursuitCheckStart(context.Background())
	if !atSweep {
		t.Error("the scratch folder was gone before the units were swept")
	}
	if _, err := os.Stat(left); err == nil {
		t.Error("the leftover scratch folder was not removed")
	}
}

// housekeepingConfiner is a verifier.Confiner that also does the housekeeping of a confiner with units outside the process.
type housekeepingConfiner struct {
	orderConfiner
	verifier.FakeConfiner
}
