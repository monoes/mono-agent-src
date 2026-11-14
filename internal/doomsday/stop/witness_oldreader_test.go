//go:build linux

package stop

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// preFormatScan is the witness reader of every release that predates the session unit, copied unchanged from the last
// release tree that had no format marker (witness.go before #1582 S1). It is the version an operator may still have installed
// beside this one (the app bundles its own CLI), so the claim "an older binary refuses a witness that holds session lines" is
// proven on THAT code and not only on this reader told to behave like an older one (witnessMaxFormat). Do not edit it to follow
// scan: its whole value is that it does not.
func preFormatScan(w Witness) (recs []Record, damaged bool) {
	b, rerr := w.Dir.fsys().ReadFile(w.path())
	if rerr != nil || len(b) < len(magic)+syncLen+macLen || !bytes.HasPrefix(b, []byte(magic)) {
		return nil, true
	}
	sync := b[len(magic) : len(magic)+syncLen]
	hm, err := w.mac([]byte("header"), sync)
	if err != nil || !bytes.Equal(hm, b[len(magic)+syncLen:len(magic)+syncLen+macLen]) {
		return nil, true
	}
	last := hm
	pos := len(magic) + syncLen + macLen
	lastSeq := map[int64]int64{}
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
		if rec.WPrev != hex.EncodeToString(last) {
			return recs, true
		}
		if rec.To != 0 && rec.T != RecAbandoned && rec.To < lastSeq[rec.Gen] {
			return recs, true
		}
		if rec.To > lastSeq[rec.Gen] {
			lastSeq[rec.Gen] = rec.To
		}
		recs = append(recs, rec)
		last = rec.mac
		pos += n
	}
	return recs, false
}

// The release gate for witness format 2: the code of a release before the session unit REFUSES (damaged, not misread) a witness
// holding any of the session lines, whichever of them came first, and it still reads a witness that never held one. A change
// to the marker's chain link that this old code could follow would let an older binary classify unit-leader lines as transitions.
func TestPreFormatReaderRefusesAWitnessThatHoldsSessionLines(t *testing.T) {
	lines := map[string]func(r *seqRig) error{
		"unit-leader":        func(r *seqRig) error { return r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}) },
		"unit-session-empty": func(r *seqRig) error { return r.seq.RetireLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}) },
		"unit-sealed": func(r *seqRig) error {
			mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindSession, Generation: 3, CgroupID: 42, Name: "n"}))
			return r.seq.sealSessionUnits(3)
		},
	}
	for name, write := range lines {
		r := newSeqRig(t)
		if _, damaged := preFormatScan(r.seq.W); damaged {
			t.Fatalf("%s: the frozen reader refused the witness before any session line", name)
		}
		mustNoErr(t, write(r))
		if recs, damaged := preFormatScan(r.seq.W); !damaged {
			t.Fatalf("%s: a release before the session unit read a witness holding session lines (%d records)", name, len(recs))
		}
		// and it cannot append either: its append starts from its scan
		if _, err := r.seq.Leaders(3); err != nil {
			t.Fatalf("%s: this release cannot read its own witness: %v", name, err)
		}
	}
}

func TestPreFormatReaderStillReadsAWitnessWithNoSessionUnit(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.Bound(UnitRecord{Kind: KindCgroup2, Generation: 3, CgroupID: 7, Name: "c"}))
	mustNoErr(t, r.seq.UnitEnded(UnitRecord{Generation: 3, CgroupID: 7}, "rmdir"))
	recs, damaged := preFormatScan(r.seq.W)
	if damaged || len(recs) < 4 {
		t.Fatalf("damaged=%v records=%d", damaged, len(recs))
	}
}

// The first session line is what upgrades the witness: nothing before it is rewritten, so a witness whose run ended before the
// first session line is read by every version up to the marker.
func TestFormatMarkerIsTheOnlyRecordAnOlderReaderCannotFollow(t *testing.T) {
	r := newSeqRig(t)
	mustNoErr(t, r.seq.AddLeader(3, Leader{PID: 500, Stamp: "1.000001", Unit: 42}))
	recs, damaged := preFormatScan(r.seq.W)
	if !damaged {
		t.Fatal("not refused")
	}
	for _, rec := range recs {
		if isFormat2Line(rec.T) || rec.T == RecFormat {
			t.Fatalf("the old reader accepted %q before refusing", rec.T)
		}
	}
}
