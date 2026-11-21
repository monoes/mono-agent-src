package stop

import (
	"errors"
	"strings"
)

// WITNESS FORMAT 2. The session unit adds lines (unit-leader, unit-sealed, unit-session-empty) that an older binary would read as
// transitions of the generation and misclassify, so a witness that holds them must be refused by an older binary, not
// misread. Rewriting the file's magic would need an in-place write the run directory has no seam for; instead the first
// format-2 line is preceded by a marker record whose chain link carries a tag ("v2:" before the previous record's MAC). An
// older reader sees an authenticated record that does not chain and refuses with ErrWitnessDamaged, which is exactly its
// answer to a replay or a fork; a reader of this version accepts the tag. Every format-2 line before a marker is damage.
// Witnesses that never held a session unit (every platform today) never get a marker and stay readable by every version.

// formatTag is the prefix of the marker's chain link.
const formatTag = "v2:"

// witnessMaxFormat is the newest witness format this binary reads. Tests lower it to 1 to simulate an older binary.
var witnessMaxFormat = 2

// ErrWitnessFormat means the witness is of a newer format than this binary reads: nothing is read or written.
var ErrWitnessFormat = errors.New("stop: the witness is of a newer format than this version reads; use the version that wrote it")

func isFormat2Line(t string) bool {
	return t == RecUnitLeader || t == RecUnitSealed || t == RecUnitSessionEmpty
}

// ensureFormat2 appends the marker when the witness has none. The caller holds the witness lock and passes the records it
// just read; a format-1 witness is upgraded by exactly this one line.
func (w Witness) ensureFormat2(recs []Record) error {
	for _, r := range recs {
		if r.T == RecFormat {
			return nil
		}
	}
	return w.appendTagged(Record{T: RecFormat}, formatTag)
}

// lineV2 appends a format-2 line under the witness lock after check accepted the records read under that lock; the marker
// is written first when it is not there yet. check may be nil; an error from it refuses the line.
func (s *Sequencer) lineV2(t string, gen uint64, extra string, check func(recs []Record) error) error {
	unlock, err := s.W.lockBounded()
	if err != nil {
		return err
	}
	defer unlock()
	recs, err := s.W.Records()
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(recs); err != nil {
			return err
		}
	}
	if err := s.W.ensureFormat2(recs); err != nil {
		return err
	}
	return s.W.Append(Record{T: t, Gen: int64(gen), Extra: extra})
}

// endsUnit reports an end or seal line of that unit in the generation.
func endsUnit(recs []Record, gen uint64, unit string) bool {
	for _, r := range recs {
		if (r.T == RecUnitEnded || r.T == RecUnitSealed) && uint64(r.Gen) == gen && (r.Extra == unit || strings.HasPrefix(r.Extra, unit+" ")) {
			return true
		}
	}
	return false
}
