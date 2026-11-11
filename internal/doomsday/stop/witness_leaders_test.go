//go:build linux

package stop

import (
	"errors"
	"testing"
)

func TestSequencerIsTheLeaderLedgerOfTheWitness(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", At: 9}))
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 501, Stamp: "2.000002"}))
	mustNoErr(t, r.seq.AddLeader(4, Leader{PID: 777, Stamp: "3.000003"}))
	got, err := r.seq.Leaders(3)
	if err != nil || len(got) != 2 || got[0].PID != 500 || got[0].At != 9 || got[1].PID != 501 {
		t.Fatalf("%+v %v", got, err)
	}
	if other, _ := r.seq.Leaders(4); len(other) != 1 || other[0].PID != 777 {
		t.Fatalf("another generation's leader leaked: %+v", other)
	}
	if none, err := r.seq.Leaders(5); err != nil || len(none) != 0 {
		t.Fatalf("%+v %v", none, err)
	}
}

// A leader line is a unit line: the freshness classification does not see it (it would otherwise read as an unknown
// transition of the generation).
func TestLeaderLinesAreTransparentToClassification(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001"}))
	recs, err := r.seq.W.Records()
	mustNoErr(t, err)
	if hasGeneration(recs, 3) {
		t.Fatal("a leader line made the generation look entered")
	}
	for _, rec := range forGeneration(recs, 3) {
		if rec.T == RecUnitLeader {
			t.Fatal("a leader line reached the classification")
		}
	}
}

// An incomplete ledger must never pass for a complete one: a line that does not decode, or a witness that cannot be
// read, is an error.
func TestLeaderLedgerFailsClosed(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001"}))
	mustNoErr(t, r.seq.UnitLine(RecUnitLeader, 3, "not json"))
	if got, err := r.seq.Leaders(3); err == nil {
		t.Fatalf("a corrupt leader line gave a list: %+v", got)
	}
	r2 := newSeqRig(t)
	r2.fs.failRead["witness"] = true
	if got, err := r2.seq.Leaders(3); err == nil {
		t.Fatalf("an unreadable witness gave a list: %+v", got)
	}
}

// The witness ledger plugs into the unit through the Kernel the wiring hands it.
func TestSessionUnitReadsItsLeadersThroughTheKernelLedger(t *testing.T) {
	r := newSeqRig(t)
	k := WithLeaderLedger(newFakeKernel(), r.seq)
	c, ok := newSessContainment(k).(sessContainment)
	if !ok || c.led == nil {
		t.Fatalf("the kernel's ledger was not found: %+v", c)
	}
	if c2, _ := newSessContainment(newFakeKernel()).(sessContainment); c2.led != nil {
		t.Fatal("a kernel without a ledger has one")
	}
	if c3, _ := newSessContainment(nil).(sessContainment); c3.led != nil {
		t.Fatal("a nil kernel has a ledger")
	}
}

// A leader is never recorded after the end of its unit: the end and the append are ordered by the witness lock, so a leader
// is either seen by the end that follows it or refused here (otherwise it would run with no way for Stop to reach it).
func TestLeaderIsRefusedAfterItsUnitEnded(t *testing.T) {
	r := newSeqRig(t)
	unit := UnitRecord{Generation: 3, CgroupID: 42}
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	mustNoErr(t, r.seq.UnitEnded(unit, "best-effort"))
	if err := r.seq.AddLeader(3, Leader{PID: 501, Stamp: "2.000002", Unit: 42}); !errors.Is(err, ErrUnitEnded) {
		t.Fatalf("a leader of an ended unit was recorded: %v", err)
	}
	got, _ := r.seq.Leaders(3)
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	// another unit of the generation, and another generation, are not affected
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 502, Stamp: "3.000003", Unit: 43}))
	mustNoErr(t, r.seq.AddLeader(4, Leader{PID: 503, Stamp: "4.000004", Unit: 42}))
}
