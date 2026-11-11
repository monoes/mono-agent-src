package stop

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The leader ledger of a session unit (sessunit.go) over the witness: one authenticated line per session leader the host
// started for the run, written before the leader's turn is released. The Sequencer is a LeaderLedger; the Kernel the
// wiring hands the session unit carries it (WithLeaderLedger).
var _ LeaderLedger = (*Sequencer)(nil)

// AddLeader appends the leader line under the witness lock, fsynced. A witness that cannot take it refuses, and so does a
// unit that is sealed or ended: the seal is written under the same lock BEFORE the end proves the unit (sealSessionUnits,
// called by the witness ender), so a leader is either on the ledger before the proof reads it or refused here. Without the
// seal a leader recorded between the proof's last scan and the end line would run in a unit reported as ended.
func (s *Sequencer) AddLeader(gen uint64, l Leader) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	unlock, err := s.W.lockBounded()
	if err != nil {
		return err
	}
	defer unlock()
	recs, err := s.W.Records()
	if err != nil {
		return err
	}
	want := fmt.Sprintf("id=%d", l.Unit) // UnitEnded: "id=<unit id> how=...", UnitSealed: "id=<unit id>"
	for _, r := range recs {
		if (r.T == RecUnitEnded || r.T == RecUnitSealed) && uint64(r.Gen) == gen && (r.Extra == want || strings.HasPrefix(r.Extra, want+" ")) {
			return ErrUnitEnded
		}
	}
	return s.W.Append(Record{T: RecUnitLeader, Gen: int64(gen), Extra: string(b)})
}

// sealIfEnding seals the session units of the generation when the end is not sparing a begun operation (exclude empty): a
// begun operation's units stay open because the unit is not over.
func (s *Sequencer) sealIfEnding(gen uint64, exclude []uint64) error {
	if len(exclude) > 0 {
		return nil
	}
	return s.sealSessionUnits(gen)
}

// sealSessionUnits writes the seal line of every session unit of the generation that is neither sealed nor ended. Units of
// the other kinds get no line (their end is unchanged). A witness that cannot be read is an error, not "nothing to seal".
func (s *Sequencer) sealSessionUnits(gen uint64) error {
	recs, err := s.W.Records()
	if err != nil {
		return err
	}
	sealed := map[string]bool{}
	for _, r := range recs {
		if r.T == RecUnitSealed && uint64(r.Gen) == gen {
			sealed[r.Extra] = true
		}
	}
	for _, rec := range s.BoundUnits(gen) {
		extra := fmt.Sprintf("id=%d", rec.CgroupID)
		if rec.Kind != KindSession || sealed[extra] {
			continue
		}
		if err := s.UnitLine(RecUnitSealed, gen, extra); err != nil {
			return err
		}
	}
	return nil
}

// sealFailed is the answer of an end whose seal could not be written: the kill ran, but a unit that could not be sealed is
// not recorded as ended (a leader could still be adopted after the proof), so a clean end becomes NOT PROVEN.
func sealFailed(out UnitOutcome, err error) UnitOutcome {
	note := "the session unit could not be sealed against a new leader, so its end is not recorded: " + err.Error()
	switch out.Kind {
	case UnitEnded, UnitEndedWithUnknown, UnitBestEffort:
		return UnitOutcome{Kind: UnitNotProven, Survivors: out.Survivors, Unknown: out.Unknown, Note: note}
	}
	if out.Note != "" {
		note = out.Note + "; " + note
	}
	out.Note = note
	return out
}

// Leaders returns the leaders of a generation in the order they were written. A witness that cannot be read, or a line
// that does not decode, is an error and never a shorter list: a leader that is not listed is not signalled, so an
// incomplete list must not pass for a complete one.
func (s *Sequencer) Leaders(gen uint64) ([]Leader, error) {
	recs, err := s.W.Records()
	if err != nil {
		return nil, err
	}
	var out []Leader
	for _, r := range recs {
		if r.T != RecUnitLeader || uint64(r.Gen) != gen {
			continue
		}
		var l Leader
		if err := json.Unmarshal([]byte(r.Extra), &l); err != nil {
			return nil, fmt.Errorf("a leader line of generation %d does not decode: %w", gen, err)
		}
		out = append(out, l)
	}
	return out, nil
}
