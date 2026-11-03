package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// Errors of the finish ledger.
var (
	// ErrFinishState: the finish is not in a state that takes this row (the message names the state).
	ErrFinishState = errors.New("doomsday: the finish is not in a state that takes this row")
	// ErrFinishMoved: the chain moved, or the run left the state, between the read and the conditional insert.
	ErrFinishMoved = errors.New("doomsday: the chain moved under the finish; nothing was inserted")
	// ErrFinishRow: the row is not bound to the current round, names no criterion of the charter, or repeats one.
	ErrFinishRow = errors.New("doomsday: the finish row is not valid for this round")
)

// FinishLedger is the finish.Ledger over the real store for one generation. The commit is decided inside the store's
// transaction: the facts are read through a verified chain, finish.Evaluate runs on them, and the finish-commit row is a
// conditional insert that also requires, in the statement, that the run is the finishing one, that nothing was committed,
// stopped or parked since the freeze, that no work-admitting row exists after the close point, and that the chain head
// is still the one the facts were read at.
type FinishLedger struct {
	s          *SQLStore
	profile    string
	generation int64
	// Hook names crash points inside the commit ("commit.read" after the facts, "commit.inserted" after the row and
	// before the transaction commits); a test makes one fail to model the process dying there.
	Hook func(point string) error
}

var _ finish.Ledger = (*FinishLedger)(nil)

// FinishLedger returns the ledger of one generation.
func (s *SQLStore) FinishLedger(profile string, generation int64) *FinishLedger {
	return &FinishLedger{s: s, profile: profile, generation: generation}
}

func (l *FinishLedger) at(p string) error {
	if l.Hook != nil {
		return l.Hook(p)
	}
	return nil
}

// Facts reads the facts of the finish from a verified chain.
func (l *FinishLedger) Facts(ctx context.Context) (finish.Facts, error) {
	v, err := l.s.loadFinishView(ctx, l.s.db, l.profile, l.generation)
	if err != nil {
		return finish.Facts{}, err
	}
	return v.facts, nil
}

// finishGuard is the part of every finish write's condition that is about the run and the closed ends of the finish: the
// run is quiescent with cause=finish, and no commit or operator stop exists.
func (l *FinishLedger) finishGuard() (string, []any) {
	return `EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'quiescent' AND cause = 'finish')
AND NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind IN ('finish.commit','finish.stop'))`,
		[]any{l.profile, l.generation, l.profile, l.generation}
}

// write runs one finish write: build checks the view and returns the payload, then the row is a conditional insert bound
// to the head that was read.
func (l *FinishLedger) write(ctx context.Context, kind string, build func(v *finishView) (any, error), extraCond string, extraArgs func(v *finishView) []any) error {
	tx, err := l.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := l.s.loadFinishView(ctx, tx, l.profile, l.generation)
	if err != nil {
		return err
	}
	payload, err := build(v)
	if err != nil {
		return err
	}
	if err := l.insert(ctx, tx, v, kind, payload, extraCond, extraArgs); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *FinishLedger) insert(ctx context.Context, tx *sql.Tx, v *finishView, kind string, payload any, extraCond string, extraArgs func(v *finishView) []any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	cond, args := l.finishGuard()
	if extraCond != "" {
		cond += " AND " + extraCond
		args = append(args, extraArgs(v)...)
	}
	_, ok, err := l.s.appendAuditIf(ctx, tx, l.profile, l.generation, kind, string(body), v.head, cond, args...)
	if err != nil {
		return err
	}
	if !ok {
		return ErrFinishMoved
	}
	return nil
}

func needState(v *finishView, want string) error {
	if v.facts.State != want {
		return fmt.Errorf("%w: it is %s, this needs %s", ErrFinishState, v.facts.State, want)
	}
	return nil
}

// Freeze records the freeze point and starts the next verification round: the run must be quiescent with cause=finish,
// and either never frozen or parked in the last round, with rounds left. It returns the round number.
func (l *FinishLedger) Freeze(ctx context.Context, digest, snapshot string) (int64, error) {
	var round int64
	err := l.write(ctx, kindFinishFreeze, func(v *finishView) (any, error) {
		if v.run.State != StateQuiescent || v.run.Cause != FinishCause {
			return nil, fmt.Errorf("%w: only a quiescent finish can freeze", ErrFinishState)
		}
		if st := v.facts.State; st != string(StateQuiescent) && st != finish.StateParked {
			return nil, fmt.Errorf("%w: it is %s", ErrFinishState, st)
		}
		if digest == "" || len(digest) > 200 || len(snapshot) > 200 {
			return nil, fmt.Errorf("%w: a freeze needs a digest", ErrFinishRow)
		}
		round = int64(len(v.freezes)) + 1
		if round > MaxFinishRounds {
			return nil, fmt.Errorf("%w: %d rounds are the most", ErrFinishState, MaxFinishRounds)
		}
		return freezeRow{Round: round, Digest: digest, Snapshot: snapshot}, nil
	}, "", nil)
	return round, err
}

// RecordEvidence appends one evidence row. A non-advisory row must carry the current round's freeze point and digest,
// name a machine criterion of the charter and be the only one for it in the round; the store sets the generation,
// the charter hash and the criterion hash, so a caller cannot bind a row to anything else. An advisory row (a pre-flight
// result) is allowed while the run is running and can never satisfy a finish.
func (l *FinishLedger) RecordEvidence(ctx context.Context, e finish.Evidence) error {
	if e.Advisory {
		return l.recordAdvisory(ctx, e)
	}
	return l.write(ctx, kindFinishEvidence, func(v *finishView) (any, error) {
		if err := needState(v, finish.StateVerifying); err != nil {
			return nil, err
		}
		c, ok := v.criterion(e.CriterionID)
		if !ok || c.Rubric || !v.bound(e.Round, e.FreezeSeq, e.Digest) {
			return nil, ErrFinishRow
		}
		if !slices.Contains([]string{"pass", "fail", "error", "timeout", "oversize", "tainted", "tampered"}, e.Result) {
			return nil, fmt.Errorf("%w: result %q", ErrFinishRow, e.Result)
		}
		for _, old := range v.facts.Evidence {
			if !old.Advisory && old.Round == e.Round && old.CriterionID == e.CriterionID {
				return nil, fmt.Errorf("%w: %s already has evidence in round %d", ErrFinishRow, e.CriterionID, e.Round)
			}
		}
		e.Seq, e.Generation, e.CharterHash, e.CriterionHash = 0, l.generation, v.facts.Charter.Hash, c.Hash
		return e, nil
	}, "", nil)
}

func (l *FinishLedger) recordAdvisory(ctx context.Context, e finish.Evidence) error {
	tx, err := l.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := l.s.loadFinishView(ctx, tx, l.profile, l.generation)
	if err != nil {
		return err
	}
	c, ok := v.criterion(e.CriterionID)
	if v.run.State != StateRunning || !ok || c.Rubric {
		return fmt.Errorf("%w: advisory evidence is for a machine criterion while the run is running", ErrFinishRow)
	}
	e.Seq, e.Generation, e.CharterHash, e.CriterionHash, e.Round, e.FreezeSeq, e.Digest = 0, l.generation, v.facts.Charter.Hash, c.Hash, 0, 0, ""
	body, _ := json.Marshal(e)
	_, ok, err = l.s.appendAuditIf(ctx, tx, l.profile, l.generation, kindFinishEvidence, string(body), v.head,
		`EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'running')`, l.profile, l.generation)
	if err != nil {
		return err
	}
	if !ok {
		return ErrFinishMoved
	}
	return tx.Commit()
}

// RecordDecision appends an operator rubric decision bound to the current round and to a rubric criterion.
func (l *FinishLedger) RecordDecision(ctx context.Context, d finish.RubricDecision) error {
	return l.write(ctx, kindFinishDecision, func(v *finishView) (any, error) {
		if err := needState(v, finish.StateVerifying); err != nil {
			return nil, err
		}
		c, ok := v.criterion(d.CriterionID)
		if !ok || !c.Rubric || !v.bound(d.Round, d.FreezeSeq, d.Digest) {
			return nil, ErrFinishRow
		}
		d.Generation, d.CharterHash, d.CriterionHash = l.generation, v.facts.Charter.Hash, c.Hash
		return d, nil
	}, "", nil)
}

// RecordRecheck appends the result of the live recheck (note 13.6 section 7.3) for the current round.
func (l *FinishLedger) RecordRecheck(ctx context.Context, ok bool) error {
	return l.write(ctx, kindFinishRecheck, func(v *finishView) (any, error) {
		if err := needState(v, finish.StateVerifying); err != nil {
			return nil, err
		}
		return recheckRow{Round: v.facts.Round.N, OK: ok}, nil
	}, "", nil)
}

// RecordReview appends a started or complete review row (finish.Ledger). The row must be bound to the current round and
// freeze point; a complete row needs a started one of the round without a complete one after it; the verdict is from the
// closed enumeration; the store sets the generation and charter hash.
func (l *FinishLedger) RecordReview(ctx context.Context, r finish.Review) error {
	return l.write(ctx, kindFinishReview, func(v *finishView) (any, error) {
		if err := needState(v, finish.StateVerifying); err != nil {
			return nil, err
		}
		if !v.bound(r.Round, r.FreezeSeq, r.Digest) {
			return nil, ErrFinishRow
		}
		switch r.State {
		case finish.ReviewStarted:
			r.Verdict, r.ReasonCode, r.ResultsDigest, r.Observed = "", "", "", finish.Pin{}
		case finish.ReviewComplete:
			if r.Verdict != finish.VerdictOK && r.Verdict != finish.VerdictBlock {
				return nil, fmt.Errorf("%w: verdict outside the closed enumeration", ErrFinishRow)
			}
			started, done := 0, 0
			for _, old := range v.facts.Reviews {
				if v.reviewInRound(old) {
					if old.State == finish.ReviewStarted {
						started++
					} else {
						done++
					}
				}
			}
			if started <= done {
				return nil, fmt.Errorf("%w: a complete review needs its started row", ErrFinishRow)
			}
		default:
			return nil, ErrFinishRow
		}
		r.Seq, r.Generation, r.CharterHash = 0, l.generation, v.facts.Charter.Hash
		return r, nil
	}, "", nil)
}

// Abort records an operator Stop during verification: nothing can commit or park afterwards (note 13.6 section 7.1).
func (l *FinishLedger) Abort(ctx context.Context, by string) error {
	return l.write(ctx, kindFinishStop, func(v *finishView) (any, error) {
		if v.run.State != StateQuiescent || v.run.Cause != FinishCause || len(v.freezes) == 0 {
			return nil, fmt.Errorf("%w: only a finish that has frozen can be stopped", ErrFinishState)
		}
		if st := v.facts.State; st == finish.StateFinished || st == StateStopped {
			return nil, fmt.Errorf("%w: it is %s", ErrFinishState, st)
		}
		return map[string]string{"by": by}, nil
	}, "", nil)
}

func (v *finishView) criterion(id string) (finish.Criterion, bool) {
	for _, c := range v.facts.Charter.Criteria {
		if c.ID == id {
			return c, true
		}
	}
	return finish.Criterion{}, false
}

func (v *finishView) bound(round, freezeSeq int64, digest string) bool {
	r := v.facts.Round
	return round == r.N && freezeSeq == r.FreezeSeq && digest == r.Digest && r.N > 0
}

func (v *finishView) reviewInRound(r finish.Review) bool {
	f := v.facts
	return r.Round == f.Round.N && r.FreezeSeq == f.Round.FreezeSeq && r.Digest == f.Round.Digest
}
