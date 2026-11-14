//go:build linux

package stop

import "testing"

// Today macOS still starts the process-group unit, so no darwin end reads or writes the witness for a seal; the day
// platformKinds["darwin"] names the session unit (S3/S4) the seal is live on darwin with nothing else to change. Both halves.
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
	t.Run("today darwin names the process-group unit: no seal", func(t *testing.T) {
		if platformKinds["darwin"] != KindPGroup {
			t.Fatalf("platformKinds[darwin] = %q: S2 flips nothing; whoever flips it updates this test", platformKinds["darwin"])
		}
		if n := endAs(t); n != 0 {
			t.Fatalf("%d seal lines on a host that runs no session unit", n)
		}
	})
	t.Run("darwin naming the session unit: sealed once", func(t *testing.T) {
		platformKindsAs(t, "darwin", KindSession)
		if n := endAs(t); n != 1 {
			t.Fatalf("%d seal lines on a darwin session host", n)
		}
	})
}
