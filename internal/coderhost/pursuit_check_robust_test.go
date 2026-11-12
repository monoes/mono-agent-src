package coderhost

import (
	"context"
	"github.com/monoes/mono-agent/internal/doomsday/finish"
	"github.com/monoes/mono-agent/internal/missionpursuit/verifier"
	"os"
	"path/filepath"
	"testing"
	"time"
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

// An unchanged project is not digested again: the stat manifest is the cheap signal, and the full digest runs once. Revert proof:
// drop the signal and the digest runs at every preparation.
func TestAnUnchangedProjectIsDigestedOnceAndNotAgain(t *testing.T) {
	var n int
	old := digestTree
	digestTree = func(ctx context.Context, root, to string) (finish.Tree, error) { n++; return old(ctx, root, to) }
	t.Cleanup(func() { digestTree = old })
	w := newCW(t)
	w.untilChecked(1)
	after := n
	for i := 0; i < 12; i++ {
		w.step()
	}
	if n != after || w.checkRows() != 1 {
		t.Fatalf("full digests %d -> %d, checks %v: an unchanged project was digested again", after, n, w.checks())
	}
	w.touch("changed")
	w.untilChecked(2)
	if n != after+1 {
		t.Errorf("a changed project is digested once: %d -> %d", after, n)
	}
}

// Reviewer failures count across away windows (two checks to a window, so a count inside one could never reach three): two bad
// runs, the person acts, a third bad run stops the checks, and the person's next action lifts it.
func TestTheReviewerFailingStopFiresAcrossAwayWindows(t *testing.T) {
	w := newCW(t)
	w.pass()
	w.wait() // the preparation this pass started reads the clock in its own goroutine: it must have read it before the clock moves
	w.rawCheck(1, "blocked", "reviewer-unavailable", 500000, 0)
	w.rawCheck(2, "blocked", "reviewer-invalid", 500000, 0)
	w.touch("two") // the project changes, so the host's own preparations never find a quiet tree and add a round of their own: the rows are the test's
	w.advance(61 * time.Minute)
	w.personActs() // a new away window: the count of this window is 0, the two earlier failures are still in a row
	w.advance(5 * time.Second)
	w.rawCheck(3, "blocked", "reviewer-pin-changed", 500000, 0)
	w.step()
	if got := w.waiting(); got != pursuitWaitingVerifier {
		t.Fatalf("waiting = %q after three bad runs in a row over two windows, want %q", got, pursuitWaitingVerifier)
	}
	w.advance(61 * time.Minute)
	w.touch("four")
	for i := 0; i < 8; i++ {
		w.step()
	}
	if w.checkRows() != 3 || len(w.requests()) != 0 {
		t.Fatalf("a check ran while the checker is failing: %v", w.checks())
	}
	w.personActs()
	w.step()
	if got := w.waiting(); got == pursuitWaitingVerifier {
		t.Error("the person acted and the stop is still there")
	}
}

// ~/.MonoAgent is ~/.monoagent where the file system ignores case (macOS, Windows); on Linux it is another folder. The compare
// function is run for both, so this holds on every platform.
func TestPathsCompareWithoutCaseWhereTheFileSystemDoes(t *testing.T) {
	if !pathWithinFold("/Users/me/.monoagent", "/Users/me/.MonoAgent/vault", true) {
		t.Error("with folding, ~/.MonoAgent/vault is inside ~/.monoagent")
	}
	if pathWithinFold("/home/me/.monoagent", "/home/me/.MonoAgent/vault", false) {
		t.Error("without folding, the two are different folders")
	}
	old := foldCase
	foldCase = true
	t.Cleanup(func() { foldCase = old })
	f := ForbiddenFolders{Trees: []string{"/nonexistent-home/.monoagent"}}
	if why := SubjectForbidden("/nonexistent-home/.MonoAgent/x", f); why == "" {
		t.Error("a differently cased state folder was accepted where case is ignored")
	}
}

// An unchanged manifest is trusted for ten minutes only: then the tree is read in full once more (a rewrite that keeps size and
// mtime must not keep an old result for ever). Revert proof: remove the age rule and the digest count never moves.
func TestAnUnchangedProjectIsReadInFullAgainAfterTenMinutes(t *testing.T) {
	var n int
	old := digestTree
	digestTree = func(ctx context.Context, root, to string) (finish.Tree, error) { n++; return old(ctx, root, to) }
	t.Cleanup(func() { digestTree = old })
	w := newCW(t)
	w.untilChecked(1)
	base := n
	for i := 0; i < 4; i++ {
		w.step()
	}
	if n != base {
		t.Fatalf("digests %d -> %d inside the ten minutes", base, n)
	}
	w.advance(11 * time.Minute)
	w.step()
	w.step()
	if n != base+1 || w.checkRows() != 1 {
		t.Errorf("digests %d -> %d, checks %v: want one more full read and no new check", base, n, w.checks())
	}
}
