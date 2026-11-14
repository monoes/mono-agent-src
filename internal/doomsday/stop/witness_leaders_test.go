//go:build linux

package stop

import (
	"context"
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

// sessionHost makes the test a host whose unit is the session kind (no platform names it yet): only there is a unit sealed.
func sessionHost(t *testing.T) {
	t.Helper()
	hostAs(t, "darwin")
	platformKindsAs(t, "darwin", KindSession)
}

type endStub struct{ f func() UnitOutcome }

func (e endStub) End(context.Context, uint64, []uint64) UnitOutcome { return e.f() }

// The end seals a session unit BEFORE it proves it: a leader recorded after the proof's last scan would run in a unit the
// end line then reports as ended. The leader is refused during the inner end, after it, and whatever the inner end answered.
func TestEndSealsASessionUnitBeforeItIsProven(t *testing.T) {
	sessionHost(t)
	for name, answer := range map[string]UnitOutcome{
		"ended":      {Kind: UnitBestEffort},
		"not proven": {Kind: UnitNotProven},
		"stragglers": {Kind: UnitStragglers},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSeqRig(t)
			unit := UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}
			mustNoErr(t, r.seq.Bound(unit))
			var during error
			en := NewWitnessEnder(endStub{func() UnitOutcome {
				during = r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}) // the host adopts while the end runs
				return answer
			}}, r.seq)
			if out := en.End(bg, 3, nil); out.Kind != answer.Kind {
				t.Fatalf("%+v", out)
			}
			if !errors.Is(during, ErrUnitEnded) {
				t.Fatalf("a leader was recorded while the unit was being ended: %v", during)
			}
			if err := r.seq.AddLeader(3, Leader{PID: 501, Stamp: "2.000002", Unit: 42}); !errors.Is(err, ErrUnitEnded) {
				t.Fatalf("a leader was recorded after the end: %v", err)
			}
			if got, _ := r.seq.Leaders(3); len(got) != 0 {
				t.Fatalf("%+v", got)
			}
			mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 502, Stamp: "3.000003", Unit: 43})) // another unit is not sealed
		})
	}
}

// The seal is for the end of the unit only: an end that spares a begun operation (exclude) leaves the unit open, and a unit of
// another kind gets no new line at all (the Linux and Windows ends are as they were).
func TestEndSealsNothingWhenTheUnitIsNotEnding(t *testing.T) {
	sessionHost(t)
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
	en := NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitStragglers} }}, r.seq)
	en.End(bg, 3, []uint64{9})
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))

	r2 := newSeqRig(t)
	mustNoErr(t, r2.seq.Bound(UnitRecord{Kind: KindCgroup2, Generation: 3, CgroupID: 7, Name: "c"}))
	before, _ := r2.seq.W.Records()
	NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitStragglers} }}, r2.seq).End(bg, 3, nil)
	after, _ := r2.seq.W.Records()
	if len(after) != len(before) {
		t.Fatalf("a cgroup unit's end wrote %d extra witness line(s)", len(after)-len(before))
	}
}

// A seal that cannot be written (the witness refuses, or cannot even be read) never skips the kill, and never lets the unit
// be recorded as ended: a clean end becomes NOT PROVEN, an unclean one keeps its kind and gains the reason.
func TestSealFailureRunsTheEndButTheUnitIsNotRecordedAsEnded(t *testing.T) {
	sessionHost(t)
	for name, fail := range map[string]func(r *seqRig){
		"append refused": func(r *seqRig) { r.fs.failAppend = true },
		"witness unreadable": func(r *seqRig) {
			r.fs.failRead["witness"] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newSeqRig(t)
			mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
			fail(r)
			ran := false
			out := NewWitnessEnder(endStub{func() UnitOutcome { ran = true; return UnitOutcome{Kind: UnitBestEffort} }}, r.seq).End(bg, 3, nil)
			if !ran {
				t.Fatal("a failed seal skipped the kill")
			}
			if out.Kind != UnitNotProven || out.Note == "" {
				t.Fatalf("a unit that could not be sealed was recorded as ended: %+v", out)
			}
			if out := NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitNone} }}, r.seq).End(bg, 3, nil); out.Kind != UnitNotProven {
				t.Fatalf("a run whose unit could not be found, after a failed seal, is not clean: %+v", out)
			}
			out = NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitStragglers, Note: "x"} }}, r.seq).End(bg, 3, nil)
			if out.Kind != UnitStragglers || out.Note == "x" || out.Note == "" {
				t.Fatalf("%+v", out)
			}
		})
	}
}

// Every attempt of a retried Stop ends the unit again: the seal is written once.
func TestSealIsWrittenOncePerUnit(t *testing.T) {
	sessionHost(t)
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
	en := NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitStragglers} }}, r.seq)
	for i := 0; i < 3; i++ {
		en.End(bg, 3, nil)
	}
	recs, _ := r.seq.W.Records()
	n := 0
	for _, rec := range recs {
		if rec.T == RecUnitSealed {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d seal lines after three attempts", n)
	}
}

// Off a session host (every platform today) the end neither reads nor writes the witness for a seal: a bound session record
// there gets no line, and an unreadable witness does not turn a clean end into NOT PROVEN.
func TestSealIsInactiveOffTheSessionHost(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "u"}))
	before, _ := r.seq.W.Records()
	if out := NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitEnded} }}, r.seq).End(bg, 3, nil); out.Kind != UnitEnded {
		t.Fatalf("%+v", out)
	}
	after, _ := r.seq.W.Records()
	for _, rec := range after[len(before):] {
		if rec.T == RecUnitSealed {
			t.Fatal("a seal was written on a host that does not run session units")
		}
	}
	r.fs.failRead["witness"] = true
	if out := NewWitnessEnder(endStub{func() UnitOutcome { return UnitOutcome{Kind: UnitEnded} }}, r.seq).End(bg, 3, nil); out.Kind != UnitEnded {
		t.Fatalf("an unreadable witness changed the end of a platform that has no session unit: %+v", out)
	}
}
