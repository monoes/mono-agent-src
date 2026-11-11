package stop

import (
	"encoding/json"
	"errors"
	"fmt"
)

// The leader ledger of a session unit (sessunit.go) over the witness: one authenticated line per session leader the host
// started for the run, written before the leader's turn is released, and one line when a recorded session is seen gone. The
// Sequencer is a LeaderLedger; the Kernel the wiring hands the session unit carries it (WithLeaderLedger). These are
// format-2 lines (witness_format.go): the first one writes the format marker an older binary refuses on.
var _ LeaderLedger = (*Sequencer)(nil)

// AddLeader appends the leader line under the witness lock, fsynced. A witness that cannot take it refuses, and so does a
// unit that is sealed or ended: the seal is written under the same lock BEFORE the end proves the unit (sealSessionUnits,
// called by the witness ender), so a leader is either on the ledger before the proof reads it or refused here. Without the
// seal a leader recorded between the proof's last scan and the end line would run in a unit reported as ended.
func (s *Sequencer) AddLeader(gen uint64, l Leader) error {
	l.Empty = false
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	return s.lineV2(RecUnitLeader, gen, string(b), func(recs []Record) error {
		if endsUnit(recs, gen, fmt.Sprintf("id=%d", l.Unit)) {
			return ErrUnitEnded
		}
		return nil
	})
}

var errAlreadyRetired = errors.New("already retired")

// RetireLeader records that the leader's session was seen gone: from then on its number is not the unit's, so processes that
// carry it after an unrelated session took it over are never taken for the unit's. Writing it twice is not an error.
func (s *Sequencer) RetireLeader(gen uint64, l Leader) error {
	l.Empty = false
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	err = s.lineV2(RecUnitSessionEmpty, gen, string(b), func(recs []Record) error {
		for _, r := range recs {
			if r.T == RecUnitSessionEmpty && uint64(r.Gen) == gen && r.Extra == string(b) {
				return errAlreadyRetired
			}
		}
		return nil
	})
	if errors.Is(err, errAlreadyRetired) {
		return nil
	}
	return err
}

// sealIfEnding seals the session units of the generation when the end is not sparing a begun operation (exclude empty): a
// begun operation's units stay open because the unit is not over. It does nothing on a host whose unit is not the session
// kind (every platform today), so no other end reads or writes the witness more than it did.
func (s *Sequencer) sealIfEnding(gen uint64, exclude []uint64) error {
	if len(exclude) > 0 || hostKind() != KindSession {
		return nil // only a host whose unit is the session kind has anything to seal: no other platform's end reads the witness more
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
		if err := s.lineV2(RecUnitSealed, gen, extra, nil); err != nil {
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
	case UnitEnded, UnitEndedWithUnknown, UnitBestEffort, UnitNone:
		return UnitOutcome{Kind: UnitNotProven, Survivors: out.Survivors, Unknown: out.Unknown, Note: note}
	}
	if out.Note != "" {
		note = out.Note + "; " + note
	}
	out.Note = note
	return out
}

// Leaders returns the leaders of a generation in the order they were written, each marked Empty when its session was seen
// gone. A witness that cannot be read, or a line that does not decode, is an error and never a shorter list: a leader that
// is not listed is not signalled, so an incomplete list must not pass for a complete one.
func (s *Sequencer) Leaders(gen uint64) ([]Leader, error) {
	recs, err := s.W.Records()
	if err != nil {
		return nil, err
	}
	var out []Leader
	empty := map[string]bool{}
	for _, r := range recs {
		if (r.T != RecUnitLeader && r.T != RecUnitSessionEmpty) || uint64(r.Gen) != gen {
			continue
		}
		var l Leader
		if err := json.Unmarshal([]byte(r.Extra), &l); err != nil {
			return nil, fmt.Errorf("a leader line of generation %d does not decode: %w", gen, err)
		}
		if r.T == RecUnitSessionEmpty {
			empty[fmt.Sprintf("%d/%d/%s", l.Unit, l.PID, l.Stamp)] = true
			continue
		}
		out = append(out, l)
	}
	for i := range out {
		out[i].Empty = empty[fmt.Sprintf("%d/%d/%s", out[i].Unit, out[i].PID, out[i].Stamp)]
	}
	return out, nil
}
