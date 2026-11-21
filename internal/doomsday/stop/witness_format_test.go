//go:build linux

package stop

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func oldReader(t *testing.T) {
	t.Helper()
	old := witnessMaxFormat
	witnessMaxFormat = 1
	t.Cleanup(func() { witnessMaxFormat = old })
}

// rawChain reads the file the way an older reader walks it: each record with its chain link as written.
func rawChain(t *testing.T, w Witness) []Record {
	t.Helper()
	b, err := w.Dir.fsys().ReadFile(w.path())
	mustNoErr(t, err)
	sync := b[len(magic) : len(magic)+syncLen]
	pos := len(magic) + syncLen + macLen
	var out []Record
	for pos < len(b) {
		i := bytes.Index(b[pos:], sync)
		if i < 0 {
			break
		}
		pos += i
		rec, n, ok := w.parseAt(b, pos)
		if !ok {
			pos++
			continue
		}
		out = append(out, rec)
		pos += n
	}
	return out
}

func TestFirstSessionLineIsPrecededByTheFormatMarkerOnce(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 501, Stamp: "2.000002", Unit: 42}))
	mustNoErr(t, r.seq.RetireLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	var types []string
	for _, rec := range rawChain(t, r.seq.W) {
		types = append(types, rec.T)
	}
	got := strings.Join(types, ",")
	if !strings.Contains(got, "format,unit-leader,unit-leader,unit-session-empty") || strings.Count(got, "format") != 1 {
		t.Fatalf("%s", got)
	}
}

// The property an older reader relies on: it accepts a record only if its chain link is the previous record's MAC. The marker's
// link carries a tag, so an older reader stops there and refuses (its answer to a replay or a fork).
func TestMarkerChainLinkIsOneAnOlderReaderCannotFollow(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	recs := rawChain(t, r.seq.W)
	for i, rec := range recs {
		if rec.T == RecFormat {
			if !strings.HasPrefix(rec.WPrev, formatTag) || i == 0 {
				t.Fatalf("%+v", rec)
			}
			if prev := recs[i-1]; strings.HasPrefix(prev.WPrev, formatTag) {
				t.Fatal("a record before the marker carries the tag")
			}
			return
		}
	}
	t.Fatal("no marker")
}

func TestOlderReaderRefusesAWitnessThatHoldsSessionLines(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	oldReader(t)
	if _, err := r.seq.W.Records(); !errors.Is(err, ErrWitnessFormat) {
		t.Fatalf("an older reader read the witness: %v", err)
	}
	if err := r.seq.W.Append(Record{T: RecPending, Gen: 3}); !errors.Is(err, ErrWitnessFormat) {
		t.Fatalf("an older writer appended to it: %v", err)
	}
	if _, err := r.seq.Leaders(3); !errors.Is(err, ErrWitnessFormat) {
		t.Fatalf("%v", err)
	}
	if err := r.seq.AddLeader(3, Leader{PID: 501, Stamp: "2.000002", Unit: 42}); !errors.Is(err, ErrWitnessFormat) {
		t.Fatalf("%v", err)
	}
	if err := r.seq.RetireLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}); !errors.Is(err, ErrWitnessFormat) {
		t.Fatalf("%v", err)
	}
}

// A witness that never held a session unit (every platform today) is written and read exactly as before, by every version.
func TestWitnessWithoutSessionUnitsGetsNoMarker(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindCgroup2, Generation: 3, CgroupID: 7, Name: "c"}))
	mustNoErr(t, r.seq.UnitEnded(UnitRecord{Generation: 3, CgroupID: 7}, "rmdir"))
	for _, rec := range rawChain(t, r.seq.W) {
		if rec.T == RecFormat || isFormat2Line(rec.T) {
			t.Fatalf("a witness with no session unit got %q", rec.T)
		}
	}
	oldReader(t)
	if _, err := r.seq.W.Records(); err != nil {
		t.Fatalf("an older reader refused a format-1 witness: %v", err)
	}
}

// A format-2 line with no marker before it is damage: a format-1 witness cannot hold one.
func TestSessionLineWithoutAMarkerIsDamage(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.W.Append(Record{T: RecUnitLeader, Gen: 3, Extra: `{"pid":500,"stamp":"1.000001","unit":42}`}))
	if _, err := r.seq.W.Records(); !errors.Is(err, ErrWitnessDamaged) {
		t.Fatalf("%v", err)
	}
}

func TestRetireRoundTripIsIdempotentAndMarksOnlyThatLeader(t *testing.T) {
	r := newSeqRig(t)
	a := Leader{PID: 500, Stamp: "1.000001", Unit: 42}
	b := Leader{PID: 501, Stamp: "2.000002", Unit: 42}
	mustNoErr(t, r.seq.AddLeader(3, a))
	mustNoErr(t, r.seq.AddLeader(3, b))
	mustNoErr(t, r.seq.RetireLeader(3, a))
	mustNoErr(t, r.seq.RetireLeader(3, a))
	got, err := r.seq.Leaders(3)
	if err != nil || len(got) != 2 || !got[0].Empty || got[1].Empty {
		t.Fatalf("%+v %v", got, err)
	}
	n := 0
	for _, rec := range rawChain(t, r.seq.W) {
		if rec.T == RecUnitSessionEmpty {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d retire lines", n)
	}
}
