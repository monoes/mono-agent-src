//go:build linux

package stop

import "testing"

// macOS starts the session unit, so a darwin end reads and writes the witness for a seal; a darwin host that named the
// process-group unit again would seal nothing. Both halves.
func TestDarwinSealsOnlyOnceItsUnitKindIsTheSessionUnit(t *testing.T) {
	endAs := func(t *testing.T) int {
		hostAs(t, "darwin")
		r := newSeqRig(t)
		mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
		NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitBestEffort} }}, r.seq).End(bg, 3, nil)
		recs, _ := r.seq.W.Records()
		n := 0
		for _, rec := range recs {
			if rec.T == RecUnitSealed {
				n++
			}
		}
		return n
	}
	t.Run("darwin names the session unit: sealed once", func(t *testing.T) {
		if platformKinds["darwin"] != KindSession {
			t.Fatalf("platformKinds[darwin] = %q, want the session unit", platformKinds["darwin"])
		}
		if n := endAs(t); n != 1 {
			t.Fatalf("%d seal lines on a darwin session host", n)
		}
	})
	t.Run("darwin naming the process-group unit: no seal", func(t *testing.T) {
		platformKindsAs(t, "darwin", KindPGroup)
		if n := endAs(t); n != 0 {
			t.Fatalf("%d seal lines on a host that runs no session unit", n)
		}
	})
}
