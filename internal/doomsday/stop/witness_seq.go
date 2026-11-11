package stop

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The sequencer (note 13.5 sections 3 and 3A) is the production use of the witness: it holds the witness lock
// across a chained transition, runs the recovery table before it, appends pending before the database write and
// committed after, keeps the anchor and the generation counter, and writes the unit lifecycle lines. Every read of
// the chained head goes through the pinned connection.

// FreshnessError is a fail-closed (or hold) verdict of the recovery table, with the row that fired.
type FreshnessError struct {
	Row    int
	Hold   bool
	Reason string
}

func (e *FreshnessError) Error() string {
	if e.Hold {
		return fmt.Sprintf("stop: freshness holds (row %d): %s", e.Row, e.Reason)
	}
	return fmt.Sprintf("stop: freshness failed closed (row %d): %s", e.Row, e.Reason)
}

// Sequencer wires the witness, the pinned connection, the anchor and the counter for one profile.
type Sequencer struct {
	W       Witness
	Pin     *Pinned // every read of the chained tables; nil refuses everything (no fallback to a pooled handle)
	Anchor  *Anchor // nil: no secret store is wired, so no run can be anchor-bound
	Counter GenCounter
	Profile string
	// Warn receives the best-effort failures that must be shown but never block (the anchor's end write).
	Warn func(string)
	Hook Hook
}

var errNoPin = errors.New("stop: no pinned connection, so the chained head cannot be read")

func (s *Sequencer) warn(msg string) {
	if s.Warn != nil {
		s.Warn(msg)
	}
}

// head reads the generation's audit head and the run state through the pinned connection.
func (s *Sequencer) head(ctx context.Context, gen int64) (h DBHead, state string, err error) {
	if s.Pin == nil {
		return h, "", errNoPin
	}
	err = s.Pin.Do(ctx, func(c *sql.Conn, _ func() error) error {
		e := c.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`,
			s.Profile, gen).Scan(&h.Seq, &h.MAC)
		if errors.Is(e, sql.ErrNoRows) {
			h = DBHead{}
		} else if e != nil {
			return e
		}
		e = c.QueryRowContext(ctx, `SELECT state FROM doomsday_runs WHERE profile = ? AND generation = ?`, s.Profile, gen).Scan(&state)
		if errors.Is(e, sql.ErrNoRows) {
			state = ""
			return nil
		}
		return e
	})
	return h, state, err
}

// forGeneration keeps the records the table classifies: dbid records and the records of this generation, without
// the transparent lines (anchor and unit lines).
func forGeneration(recs []Record, gen int64) []Record {
	var out []Record
	for _, r := range recs {
		switch r.T {
		case RecDBID:
			out = append(out, r)
		case RecAnchor, RecUnitIntent, RecUnitBound, RecUnitEnded, RecUnitLeader:
		default:
			if r.Gen == gen {
				out = append(out, r)
			}
		}
	}
	return out
}

// KindEnter names the transition that enters a generation.
const KindEnter = "enter"

func hasGeneration(recs []Record, gen int64) bool {
	for _, r := range forGeneration(recs, gen) {
		if r.Gen == gen {
			return true
		}
	}
	return false
}

func anchorBound(recs []Record, gen int64) bool {
	for _, r := range recs {
		if r.T == RecAnchor && r.Gen == gen {
			return true
		}
	}
	return false
}

// classify runs the whole freshness check of one generation under the witness lock the caller holds, and applies
// the repairs of rows 2 and 3. A nil result means the generation may proceed; the head it was proven against is returned.
func (s *Sequencer) classify(ctx context.Context, gen int64) (DBHead, error) {
	if s.W.Dir.IsRefused(uint64(gen)) {
		return DBHead{}, &FreshnessError{Row: 13, Reason: "the generation was refused by an earlier failure"}
	}
	if s.Pin != nil {
		if err := s.Pin.Check(); err != nil {
			return DBHead{}, &FreshnessError{Row: 13, Reason: "the pinned connection no longer holds the database: " + err.Error()}
		}
	}
	recs, rerr := s.W.Records()
	h, state, err := s.head(ctx, gen)
	if err != nil {
		return DBHead{}, &FreshnessError{Row: 13, Reason: "the chained head cannot be read: " + err.Error()}
	}
	if id := latestDBID(recs); rerr == nil && s.Pin != nil && id != "" && id != s.Pin.Identity().String() {
		return DBHead{}, &FreshnessError{Row: 13, Reason: "the database file is not the one the witness recorded"}
	}
	in := Freshness{Records: forGeneration(recs, gen), Damaged: rerr != nil, D: h, DBGen: gen, DBRunning: state == string(StateRunning)}
	if anchorBound(recs, gen) {
		in.AnchorBound = true
		in.Anchor = func() (AnchorState, error) {
			if s.Anchor == nil {
				return AnchorState{}, ErrNoSecretStore
			}
			return s.Anchor.Read()
		}
	}
	d := Classify(in)
	switch d.Verdict {
	case VerdictProceed:
		return h, nil
	case VerdictAbandon, VerdictCommit:
		x, _ := lastSubstantive(in.Records)
		rec := Record{T: RecAbandoned, Gen: gen, To: x.From - 1, Head: x.DBHead}
		if d.Verdict == VerdictCommit {
			rec = Record{T: RecCommitted, Gen: gen, To: x.To, Head: hex.EncodeToString(h.MAC)}
		}
		if err := s.W.Append(rec); err != nil {
			return DBHead{}, &FreshnessError{Row: d.Row, Reason: "the repair line could not be written: " + err.Error()}
		}
		return h, nil
	case VerdictHold:
		return DBHead{}, &FreshnessError{Row: d.Row, Hold: true, Reason: d.Reason}
	}
	ferr := &FreshnessError{Row: d.Row, Reason: d.Reason}
	if rerr == nil {
		// Stop never adds a chained row to a chain it cannot trust; it records the failure (refusal file first).
		if err := s.W.FailGeneration(gen, d.Row); err != nil {
			ferr.Reason += " (and the failure could not be recorded: " + err.Error() + ")"
		}
	} else if err := s.W.Dir.Refuse(uint64(gen), d.Row); err != nil {
		ferr.Reason += " (and the refusal could not be recorded: " + err.Error() + ")"
	}
	return DBHead{}, ferr
}

func latestDBID(recs []Record) string {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].T == RecDBID {
			return recs[i].Extra
		}
	}
	return ""
}

// Check is the freshness of one admission (any process): lock, classify, apply, unlock.
func (s *Sequencer) Check(ctx context.Context, gen int64) error {
	unlock, err := s.W.lockBounded()
	if err != nil {
		return fmt.Errorf("stop: the witness lock cannot be taken: %w", err)
	}
	defer unlock()
	_, err = s.classify(ctx, gen)
	return err
}

// Sequence is one chained transition: freshness, pending, fn (which writes the database rows), committed. fn must
// write at least one chained row or none (an atomic transaction). ending marks a transition to stop-requested or
// later; ended marks the one that ends the generation.
func (s *Sequencer) Sequence(ctx context.Context, gen int64, kind string, ending, ended bool, fn func(context.Context) error) error {
	return s.SequenceRows(ctx, gen, kind, ending, ended, 1, fn)
}

// SequenceRows is Sequence for a transition that writes rows chained rows in one transaction (a strategy write adds its
// row's audit lines together): the pending line covers the whole range, so a crash after the commit is classified as a
// commit (row 3), not as a database ahead of the witness (row 5). fn must write all of the rows or none.
func (s *Sequencer) SequenceRows(ctx context.Context, gen int64, kind string, ending, ended bool, rows int, fn func(context.Context) error) error {
	if rows < 1 {
		rows = 1
	}
	unlock, err := s.W.lockBounded()
	if err != nil {
		return fmt.Errorf("stop: the witness lock cannot be taken: %w", err)
	}
	defer unlock()
	if kind == KindEnter { // the first line of a generation: a header, so the table has a record to start from
		if recs, rerr := s.W.Records(); rerr == nil && !hasGeneration(recs, gen) {
			if err := s.W.Append(Record{T: RecHeader, Gen: gen}); err != nil {
				return fmt.Errorf("stop: the generation's header could not be written, so it was not entered: %w", err)
			}
		}
	}
	h, err := s.classify(ctx, gen)
	if err != nil {
		return err
	}
	pend := Record{T: RecPending, Gen: gen, From: h.Seq + 1, To: h.Seq + int64(rows), DBHead: hex.EncodeToString(h.MAC), Ending: ending, Extra: kind}
	if err := s.W.Append(pend); err != nil { // record-then-act: no pending line, no transition
		return fmt.Errorf("stop: the witness could not record the transition, so it was not made: %w", err)
	}
	s.Hook.at("seq.pending")
	ferr := fn(ctx)
	s.Hook.at("seq.written")
	h2, _, herr := s.head(ctx, gen)
	switch {
	case herr != nil:
		return errors.Join(ferr, fmt.Errorf("stop: the new head cannot be read; the pending line stays for recovery: %w", herr))
	case h2.Seq == h.Seq:
		if err := s.W.Append(Record{T: RecAbandoned, Gen: gen, To: h.Seq, Head: pend.DBHead}); err != nil {
			return errors.Join(ferr, err)
		}
		return ferr
	}
	c := Record{T: RecCommitted, Gen: gen, To: h2.Seq, Head: hex.EncodeToString(h2.MAC), Ended: ended && ferr == nil}
	if err := s.W.Append(c); err != nil {
		s.warn("the committed line could not be written; the next check repairs it (row 3): " + err.Error())
	}
	if ended && ferr == nil && s.Anchor != nil {
		if err := s.Anchor.Set(gen, AnchorEnded); err != nil {
			s.warn("the anchor could not be marked ended: " + err.Error())
		}
	}
	return ferr
}

// ----- entry -----

// ErrEntryRefused wraps every reason the entry rules refuse a generation.
var ErrEntryRefused = errors.New("stop: the entry is refused")

// committedMaxGen is the highest generation the witness shows as entered (a committed or failed record).
func committedMaxGen(recs []Record) int64 {
	var max int64
	for _, r := range recs {
		if (r.T == RecCommitted || r.T == RecFailed || r.Ended) && r.Gen > max {
			max = r.Gen
		}
	}
	return max
}

func committedMaxGenBelow(recs []Record, gen int64) int64 {
	var below []Record
	for _, r := range recs {
		if r.Gen < gen {
			below = append(below, r)
		}
	}
	return committedMaxGen(below)
}

// BeginEntry is step 1 of an entry: under the witness lock it creates the witness for the profile's first run,
// refuses a database that is behind what the outside memories remember (the counter file, the witness, the
// anchor), and classifies the previous generation. The counter reserves the number once the entry has happened
// (Entered); the store only accepts the consecutive generation, so a number wasted before the database is
// touched would lock the profile out.
func (s *Sequencer) BeginEntry(ctx context.Context, gen int64) error {
	if s.Pin == nil {
		return errNoPin
	}
	unlock, err := s.W.lockBounded()
	if err != nil {
		return fmt.Errorf("%w: the witness lock cannot be taken: %v", ErrEntryRefused, err)
	}
	defer unlock()
	recs, rerr := s.W.Records()
	if _, exists := s.W.Dir.fsys().ReadFile(s.W.path()); exists != nil {
		// no witness: only the profile's first generation may create it; later it was deleted
		if gen != 1 {
			return fmt.Errorf("%w: the witness is missing but generation %d is not the first (row 7)", ErrEntryRefused, gen)
		}
		if max, merr := s.Counter.Max(); merr != nil || max > 0 {
			return fmt.Errorf("%w: the witness is missing but the generation counter remembers earlier runs (row 7)", ErrEntryRefused)
		}
		if err := s.W.Init(); err != nil {
			return fmt.Errorf("%w: the witness cannot be created: %v", ErrEntryRefused, err)
		}
		if err := s.W.Append(Record{T: RecDBID, Extra: s.Pin.Identity().String()}); err != nil {
			return fmt.Errorf("%w: the database identity cannot be recorded: %v", ErrEntryRefused, err)
		}
		recs, rerr = s.W.Records()
	}
	if rerr != nil {
		return fmt.Errorf("%w: %v (row 7)", ErrEntryRefused, rerr)
	}
	floor, err := s.Counter.Max()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEntryRefused, err)
	}
	floor = max(floor, committedMaxGen(recs))
	if s.Anchor != nil {
		switch st, aerr := s.Anchor.Read(); {
		case errors.Is(aerr, ErrStoreLocked):
			return &FreshnessError{Row: 10, Hold: true, Reason: "the anchor store is locked; nothing is entered until it is readable"}
		case aerr == nil && st.Found:
			floor = max(floor, st.Generation)
		}
	}
	if gen <= floor {
		return fmt.Errorf("%w: the database offers generation %d but the counter, the witness or the anchor remember generation %d: the database was rolled back", ErrEntryRefused, gen, floor)
	}
	if gen > 1 {
		// A previous generation that failed closed has a durable refusal: that is the proof that it is over (note
		// 13.5, "a new run is the only way forward"). The refusal stays and still blocks continuing THAT generation
		// (classify checks it first); it does not block the next one. A damaged refusal proves nothing and refuses.
		_, failed, rerr := s.W.Dir.Refusal(uint64(gen - 1))
		if rerr != nil {
			return fmt.Errorf("%w: the previous generation's refusal cannot be read: %v", ErrEntryRefused, rerr)
		}
		if failed {
			return nil
		}
		if _, err := s.classify(ctx, gen-1); err != nil {
			return fmt.Errorf("%w: the previous generation does not verify: %v", ErrEntryRefused, err)
		}
	}
	return nil
}

// Entered completes the entry after the database holds the run: the anchor in state entering, read back, then
// running, the anchor line in the witness, and the generation reserved in the counter. A secret store that cannot
// be written makes the run `anchor-bound: no`; why says so for the entry dialog. It never fails the entry.
func (s *Sequencer) Entered(ctx context.Context, gen int64) (bound bool, why string, err error) {
	unlock, lerr := s.W.lockBounded()
	if lerr != nil {
		return false, "", lerr
	}
	defer unlock()
	recs, _ := s.W.Records()
	var priorAnchor int64 // what the anchor remembered before this entry touched it
	if s.Anchor != nil {
		if st, aerr := s.Anchor.Read(); aerr == nil && st.Found {
			priorAnchor = st.Generation
		}
	}
	if s.Anchor == nil {
		why = "rollback detection is weaker: no secret store is wired on this install"
	} else if bound, why = s.Anchor.Enter(gen); bound {
		if aerr := s.W.Append(Record{T: RecAnchor, Gen: gen}); aerr != nil {
			bound, why = false, "rollback detection is weaker: the anchor line could not be written to the witness"
		} else if aerr := s.Anchor.Set(gen, AnchorRunning); aerr != nil {
			why = "the anchor could not be moved to running: " + aerr.Error()
		}
	}
	got, rerr := s.Counter.Reserve(gen-1, committedMaxGenBelow(recs, gen), priorAnchor)
	if rerr != nil {
		return bound, why, rerr
	}
	if got != gen {
		return bound, why, fmt.Errorf("stop: the generation counter reserved %d for generation %d", got, gen)
	}
	return bound, why, nil
}

// ----- the unit lifecycle lines -----

// UnitLine appends one unit line under the witness lock, fsynced. A witness that cannot take it refuses.
func (s *Sequencer) UnitLine(t string, gen uint64, extra string) error {
	unlock, err := s.W.lockBounded()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := s.W.Records(); err != nil {
		return err
	}
	return s.W.Append(Record{T: t, Gen: int64(gen), Extra: extra})
}

// Intent, Bound and Ended make the sequencer a UnitJournal (Ended is called by the witness-writing Ender).
func (s *Sequencer) Intent(gen uint64, name, nonce, boot string) error {
	return s.UnitLine(RecUnitIntent, gen, fmt.Sprintf("name=%s nonce=%s boot=%s", name, nonce, boot))
}

func (s *Sequencer) Bound(rec UnitRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return s.UnitLine(RecUnitBound, rec.Generation, string(b))
}

// UnitEnded writes the durable end line; how is "rmdir" or "absent".
func (s *Sequencer) UnitEnded(rec UnitRecord, how string) error {
	return s.UnitLine(RecUnitEnded, rec.Generation, fmt.Sprintf("id=%d how=%s", rec.CgroupID, how))
}

// BoundUnits returns the authenticated unit records of a generation (UnitEnder.Witness): the unit-bound lines
// without a later unit-ended line for the same cgroup, so a unit already proven ended is not proven again.
func (s *Sequencer) BoundUnits(gen uint64) []UnitRecord {
	recs, err := s.W.Records()
	if err != nil {
		return nil
	}
	ended := map[string]bool{}
	for _, r := range recs {
		if r.T == RecUnitEnded && uint64(r.Gen) == gen {
			if i := strings.IndexByte(r.Extra, ' '); i > 0 {
				ended[r.Extra[:i]] = true // "id=<cgroup id>"
			}
		}
	}
	var out []UnitRecord
	for _, r := range recs {
		if r.T != RecUnitBound || uint64(r.Gen) != gen {
			continue
		}
		var u UnitRecord
		if json.Unmarshal([]byte(r.Extra), &u) == nil && !ended[fmt.Sprintf("id=%d", u.CgroupID)] {
			out = append(out, u)
		}
	}
	return out
}

// witnessEnder writes unit-ended after an Ender has proven the unit gone. If the line cannot be written the
// outcome is "not proven", so the run stays cleanup-pending (Q-c of note 13.2).
type witnessEnder struct {
	Inner Ender
	Seq   *Sequencer
}

// NewWitnessEnder wraps an Ender with the unit-ended line.
func NewWitnessEnder(inner Ender, seq *Sequencer) Ender { return witnessEnder{Inner: inner, Seq: seq} }

func (e witnessEnder) End(ctx context.Context, gen uint64, exclude []uint64) UnitOutcome {
	out := e.Inner.End(ctx, gen, exclude)
	if out.Kind != UnitEnded && out.Kind != UnitEndedWithUnknown && out.Kind != UnitBestEffort {
		return out
	}
	if len(exclude) > 0 { // tickets of begun operations are still running; the unit is not over
		return out
	}
	for _, rec := range e.Seq.BoundUnits(gen) {
		if e.endedWritten(rec) {
			continue
		}
		if err := e.Seq.UnitEnded(rec, endedHow(out)); err != nil {
			return UnitOutcome{Kind: UnitNotProven, Survivors: out.Survivors, Unknown: out.Unknown,
				Note: "the unit is gone but its end line could not be written to the witness: " + err.Error()}
		}
	}
	return out
}

func (e witnessEnder) endedWritten(rec UnitRecord) bool {
	recs, err := e.Seq.W.Records()
	if err != nil {
		return false
	}
	want := fmt.Sprintf("id=%d ", rec.CgroupID)
	for _, r := range recs {
		if r.T == RecUnitEnded && uint64(r.Gen) == rec.Generation && len(r.Extra) >= len(want) && r.Extra[:len(want)] == want {
			return true
		}
	}
	return false
}

// endedHow is the label of the durable end line: "rmdir" for a cgroup whose removal proved it empty, "best-effort" for a
// unit the mechanism could not prove.
func endedHow(out UnitOutcome) string {
	if out.Kind == UnitBestEffort {
		return "best-effort"
	}
	return "rmdir"
}
