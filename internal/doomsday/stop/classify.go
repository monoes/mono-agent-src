package stop

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DBHead is the head of the database's chained stream for one generation: the last seq and its MAC.
type DBHead struct {
	Seq int64
	MAC []byte
}

// ReadDBHead reads the audit chain head directly (the store interface does not expose it; this package owns the
// witness comparison). An empty chain is seq 0 with no MAC.
func ReadDBHead(ctx context.Context, db *sql.DB, profile string, gen int64) (DBHead, error) {
	var h DBHead
	err := db.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`,
		profile, gen).Scan(&h.Seq, &h.MAC)
	if errors.Is(err, sql.ErrNoRows) {
		return DBHead{}, nil
	}
	return h, err
}

// Verdict is the action of the decision table.
type Verdict int

const (
	VerdictProceed Verdict = iota + 1
	// VerdictAbandon: append "abandoned" for the pending range, then proceed (row 2).
	VerdictAbandon
	// VerdictCommit: append "committed" for the pending range, then proceed (row 3).
	VerdictCommit
	// VerdictHold: nothing is admitted and nothing is revoked until the anchor is readable (row 10).
	VerdictHold
	// VerdictFailClosed: no work for the generation, tokens revoked, units ended, the pane says why.
	VerdictFailClosed
)

// AnchorState is the second witness in the OS secret store (best effort, not a trust root).
type AnchorState struct {
	Found      bool
	Generation int64
	Ended      bool
}

// Freshness is the input of the decision procedure of note 13.5 section 4.
type Freshness struct {
	Records     []Record
	Damaged     bool // missing/unreadable/damaged witness
	D           DBHead
	DBGen       int64
	DBRunning   bool
	AnchorBound bool
	Anchor      func() (AnchorState, error) // nil when not anchor-bound
}

// Decision is the verdict with its row number, so a test (and the pane) can name what fired.
type Decision struct {
	Verdict Verdict
	Row     int
	Reason  string
}

func fail(row int, f string, a ...any) Decision {
	return Decision{VerdictFailClosed, row, fmt.Sprintf(f, a...)}
}

// Classify runs the phases in order; a failure in an earlier phase ends the evaluation, and row 1 is reachable
// only in phase 4. The table is total: anything not matched is row 13, fail closed.
func Classify(in Freshness) Decision {
	// Phase 1: witness integrity.
	if in.Damaged || len(in.Records) == 0 {
		return fail(7, "the witness is missing, unreadable or damaged; freshness cannot be proven")
	}
	// Phase 2: ended and anchor consistency.
	for _, r := range in.Records {
		if r.Ended && in.DBRunning {
			return fail(8, "the witness says the generation ended, the database says running")
		}
	}
	if in.AnchorBound {
		a, err := in.Anchor()
		switch {
		case err != nil:
			return Decision{VerdictHold, 10, "the anchor is unreadable now; nothing is admitted until it is"}
		case !a.Found:
			return fail(11, "the anchor item is missing for an anchor-bound generation")
		case a.Generation > in.DBGen || (a.Generation == in.DBGen && a.Ended && in.DBRunning):
			return fail(9, "the anchor is newer or ended while the files say running")
		}
	}
	x, ok := lastSubstantive(in.Records)
	if !ok {
		return fail(13, "no header-level record to classify from")
	}
	if x.T == RecFailed {
		return fail(13, "the generation was refused by an earlier failure")
	}
	dhex := hex.EncodeToString(in.D.MAC)
	// Phase 3: pending.
	if x.T == RecPending {
		n, last := x.From-1, x.To
		switch {
		case in.D.Seq == n && x.DBHead == dhex && x.Ending:
			return fail(2, "a stop was being recorded when the host died (row 2e); the marker covers it")
		case in.D.Seq == n && x.DBHead == dhex:
			return Decision{VerdictAbandon, 2, "crash before the commit"}
		case in.D.Seq == last && (x.Head == "" || x.Head == dhex):
			return Decision{VerdictCommit, 3, "crash after the commit"}
		case in.D.Seq < n:
			return fail(4, "database rolled back")
		case in.D.Seq > last:
			return fail(5, "the database is ahead of the witness")
		}
		return fail(13, "pending record that the database matches neither before nor after")
	}
	// Phase 4: steady state at the last committed/abandoned/header seq.
	seq, ok := openSeq(in.Records)
	if !ok {
		return fail(13, "no open seq")
	}
	switch {
	case in.D.Seq == seq && lastCommittedHead(in.Records) != "" && lastCommittedHead(in.Records) != dhex:
		return fail(6, "same seq, different head MAC: a replaced row or a fork")
	case in.D.Seq == seq:
		return Decision{VerdictProceed, 1, "consistent"}
	case in.D.Seq < seq:
		return fail(4, "database rolled back (seq %d < witness %d)", in.D.Seq, seq)
	case in.D.Seq > seq:
		return fail(5, "database seq %d is ahead of the witness %d with no pending record", in.D.Seq, seq)
	}
	return fail(13, "unclassified")
}

// lastSubstantive skips transparent dbid records.
func lastSubstantive(rs []Record) (Record, bool) {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].T != RecDBID {
			return rs[i], true
		}
	}
	return Record{}, false
}

func openSeq(rs []Record) (int64, bool) {
	for i := len(rs) - 1; i >= 0; i-- {
		switch rs[i].T {
		case RecCommitted, RecAbandoned:
			return rs[i].To, true
		}
	}
	return 0, true // only a header/dbid: open at seq 0
}

func lastCommittedHead(rs []Record) string {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].T == RecCommitted {
			return rs[i].Head
		}
	}
	return ""
}

// ----- durable refusal (13.5 P1: a torn failure record from any writer must still leave a refusal) -----

func (r RunDir) refusedPath(gen uint64) string {
	return filepath.Join(r.Dir, fmt.Sprintf("%d.refused", gen))
}

// Refuse records, BEFORE the witness "failed" record is appended, a durable create-exclusive refusal file for
// the generation. A crash while appending "failed" (a torn record is ignored by the reader) therefore still
// leaves a refusal that every writer checks; the marker covers the Stop.
func (r RunDir) Refuse(gen uint64, row int) error {
	err := writeDurable(r.fsys(), r.Dir, r.refusedPath(gen), []byte(fmt.Sprintf("row %d\n", row)))
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

// IsRefused is checked by every writer before it admits: a present refusal is generation-terminal.
func (r RunDir) IsRefused(gen uint64) bool {
	_, err := r.fsys().ReadFile(r.refusedPath(gen))
	return err == nil || !errors.Is(err, os.ErrNotExist)
}

// Refusal reads the durable refusal of a generation: found is true only for a file that parses as the record
// Refuse writes. A missing file is (0, false, nil); an unreadable or malformed one is an error, so a caller never
// takes a damaged refusal for proof of anything.
func (r RunDir) Refusal(gen uint64) (row int, found bool, err error) {
	b, rerr := r.fsys().ReadFile(r.refusedPath(gen))
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return 0, false, nil
		}
		return 0, false, rerr
	}
	if _, serr := fmt.Sscanf(string(b), "row %d\n", &row); serr != nil {
		return 0, false, fmt.Errorf("the refusal of generation %d is malformed", gen)
	}
	return row, true, nil
}

// FailGeneration is what every writer does on a fail-closed decision: refusal file first, then the witness
// "failed" record (best effort), so the refusal does not depend on the second write.
func (w Witness) FailGeneration(gen int64, row int) error {
	if err := w.Dir.Refuse(uint64(gen), row); err != nil {
		return err
	}
	return w.Append(Record{T: RecFailed, Gen: gen, Row: row})
}
