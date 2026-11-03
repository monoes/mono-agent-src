package stop

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newAnchor(t *testing.T) (Anchor, *FileSecretStore) {
	t.Helper()
	fs := &FileSecretStore{Dir: t.TempDir()}
	return Anchor{Store: fs, Keys: testKeyer(), Profile: "p"}, fs
}

func classifyWithAnchor(a Anchor, dbGen int64, running bool) Decision {
	H := []byte{0xaa}
	return Classify(Freshness{Records: []Record{{T: RecDBID}, {T: RecCommitted, To: 5, Head: "aa"}}, D: DBHead{5, H},
		DBGen: dbGen, DBRunning: running, AnchorBound: true, Anchor: a.Func()})
}

// T26/T33: no secret store -> the run is not anchor-bound, the entry never fails, and the reason is shown.
func TestAnchorRefusesWhereNoSecretStore(t *testing.T) {
	a := Anchor{Store: NoSecretStore{}, Keys: testKeyer(), Profile: "p"}
	bound, why := a.Enter(1)
	if bound || why == "" {
		t.Fatalf("bound=%v why=%q", bound, why)
	}
	if err := a.Set(1, AnchorRunning); !errors.Is(err, ErrNoSecretStore) {
		t.Fatalf("a write went to a store that does not exist: %v", err)
	}
	// an anchor-bound generation whose store vanished is unreadable now: hold, not pass and not fail
	if d := classifyWithAnchor(a, 1, true); d.Verdict != VerdictHold || d.Row != 10 {
		t.Fatalf("%+v", d)
	}
}

func TestAnchorEntryRows9To11(t *testing.T) {
	a, fs := newAnchor(t)
	bound, why := a.Enter(2)
	if !bound {
		t.Fatal(why)
	}
	mustNoErr(t, a.Set(2, AnchorRunning))
	if d := classifyWithAnchor(a, 2, true); d.Verdict != VerdictProceed {
		t.Fatalf("a consistent anchor blocked: %+v", d)
	}
	// row 9: the files were restored to an older generation while the anchor says 2
	if d := classifyWithAnchor(a, 1, true); d.Row != 9 || d.Verdict != VerdictFailClosed {
		t.Fatalf("%+v", d)
	}
	// row 9: the generation ended (anchor), files say running
	mustNoErr(t, a.Set(2, AnchorEnded))
	if d := classifyWithAnchor(a, 2, true); d.Row != 9 {
		t.Fatalf("%+v", d)
	}
	// row 10: locked keyring holds
	fs.Locked = true
	if d := classifyWithAnchor(a, 2, true); d.Verdict != VerdictHold || d.Row != 10 {
		t.Fatalf("%+v", d)
	}
	fs.Locked = false
	// row 11: item deleted
	mustNoErr(t, os.Remove(filepath.Join(fs.Dir, "anchor-p")))
	if d := classifyWithAnchor(a, 2, true); d.Row != 11 || d.Verdict != VerdictFailClosed {
		t.Fatalf("%+v", d)
	}
}

func TestAnchorEditedItemIsWorthNothing(t *testing.T) {
	a, fs := newAnchor(t)
	a.Enter(1)
	b, _ := os.ReadFile(filepath.Join(fs.Dir, "anchor-p"))
	forged := []byte(string(b)[:len(b)-3] + `0"}`) // flip the last MAC digit
	mustNoErr(t, os.WriteFile(filepath.Join(fs.Dir, "anchor-p"), forged, 0o600))
	if d := classifyWithAnchor(a, 1, true); d.Row != 11 {
		t.Fatalf("an item with a bad MAC was believed: %+v", d)
	}
}

func TestAnchorOnlyMovesForward(t *testing.T) {
	a, _ := newAnchor(t)
	mustNoErr(t, a.Set(3, AnchorEnded))
	if err := a.Set(3, AnchorRunning); err == nil {
		t.Fatal("an ended anchor was reopened")
	}
	if err := a.Set(2, AnchorEntering); err == nil {
		t.Fatal("the anchor generation went backwards")
	}
	mustNoErr(t, a.Set(4, AnchorEntering))
}

// T13/T33: the number is max(counter, witness, anchor, own)+1 and is never reused, even after a crash or a
// restored database counter.
func TestGenCounterNeverReusesANumber(t *testing.T) {
	w := newWitness(t, nil)
	a, _ := newAnchor(t)
	c := GenCounter{Dir: w.Dir, Keys: testKeyer()}
	g1, err := c.NextGeneration(0, w, &a)
	mustNoErr(t, err)
	if g1 != 1 {
		t.Fatalf("first=%d", g1)
	}
	// crash after the reservation, before the database commit: the next entry skips the wasted number
	g2, err := c.NextGeneration(0, w, &a) // the database counter is still 0 (a restored database)
	mustNoErr(t, err)
	if g2 != 2 {
		t.Fatalf("reused or skipped wrongly: %d", g2)
	}
	// the counter file is lost: the witness remembers
	mustNoErr(t, w.Append(Record{T: RecDBID, Gen: 7, Extra: "x"}))
	mustNoErr(t, os.Remove(filepath.Join(w.Dir.Dir, "generation-counter")))
	g3, err := c.NextGeneration(0, w, nil)
	mustNoErr(t, err)
	if g3 != 8 {
		t.Fatalf("witness not consulted: %d", g3)
	}
	// the counter and the witness are lost: the anchor remembers
	mustNoErr(t, a.Set(20, AnchorRunning))
	mustNoErr(t, os.Remove(filepath.Join(w.Dir.Dir, "generation-counter")))
	g4, err := c.NextGeneration(3, Witness{Dir: w.Dir, Keys: testKeyer()}, &a)
	mustNoErr(t, err)
	if g4 != 21 {
		t.Fatalf("anchor not consulted: %d", g4)
	}
	// and the database counter is honoured when it is the highest
	g5, err := c.NextGeneration(50, w, &a)
	mustNoErr(t, err)
	if g5 != 51 {
		t.Fatalf("db counter not consulted: %d", g5)
	}
}

func TestGenCounterTornTailIgnoredDamageRefused(t *testing.T) {
	w := newWitness(t, nil)
	c := GenCounter{Dir: w.Dir, Keys: testKeyer()}
	_, err := c.Reserve(0, 0, 0)
	mustNoErr(t, err)
	_, err = c.Reserve(0, 0, 0)
	mustNoErr(t, err)
	p := filepath.Join(w.Dir.Dir, "generation-counter")
	b, _ := os.ReadFile(p)
	// a torn third record
	mustNoErr(t, os.WriteFile(p, append(b, make([]byte, 20)...), 0o600))
	if m, err := c.Max(); err != nil || m != 2 {
		t.Fatalf("%d %v", m, err)
	}
	// a complete record that does not verify
	b[len(b)-1] ^= 1
	mustNoErr(t, os.WriteFile(p, b, 0o600))
	if _, err := c.Reserve(0, 0, 0); !errors.Is(err, ErrCounterDamaged) {
		t.Fatalf("entry was allowed on a damaged counter: %v", err)
	}
}

func TestGenCounterLockedAnchorHoldsEntry(t *testing.T) {
	w := newWitness(t, nil)
	a, fs := newAnchor(t)
	fs.Locked = true
	c := GenCounter{Dir: w.Dir, Keys: testKeyer()}
	if _, err := c.NextGeneration(0, w, &a); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("%v", err)
	}
}
