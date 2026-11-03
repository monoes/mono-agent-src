package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/monoes/mono-agent/internal/doomsday/finish"
)

// The closed list of park reasons (note 13.6 section 13); the ledger records no other.
var parkReasons = []finish.Reason{finish.ReasonCriteriaFailed, finish.ReasonCriterionError, finish.ReasonTainted, finish.ReasonTampered,
	finish.ReasonSubjectChanged, finish.ReasonVerifierBlocked, finish.ReasonVerifierUnavailable, finish.ReasonVerifierChanged,
	finish.ReasonRubricRejected, finish.ReasonInternalError, finish.ReasonDeadline, finish.ReasonSubjectMoving,
	finish.ReasonEnforcementRequired}

type commitRow struct {
	Round int64 `json:"round"`
}

// Commit is the finish-commit transition (finish.Ledger). It reads the facts through a verified chain in its own
// transaction, runs finish.Evaluate on them, and inserts the commit row only on Commit, in a conditional insert (see
// insertCommit). Anything else is finish.ErrCommitRefused with nothing written. A commit that already exists is a no-op,
// so a repeat after a crash at the commit never writes a second row.
func (l *FinishLedger) Commit(ctx context.Context) error {
	tx, err := l.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := l.s.loadFinishView(ctx, tx, l.profile, l.generation)
	if err != nil {
		return err
	}
	if v.facts.State == finish.StateFinished {
		return nil
	}
	if err := l.at("commit.read"); err != nil {
		return err
	}
	if verdict := finish.Evaluate(v.facts); verdict.Outcome != finish.Commit {
		return fmt.Errorf("%w: %s (%s)", finish.ErrCommitRefused, verdict.Reason, verdict.Detail)
	}
	if err := l.insertCommit(ctx, tx, v); err != nil {
		return err
	}
	if err := l.at("commit.inserted"); err != nil {
		return err // the transaction rolls back: nothing was committed
	}
	return tx.Commit()
}

// insertCommit is the conditional insert of the finish-commit row. Besides the finish guard it requires, in the one
// statement: no park since the freeze, and no row of any work-admitting kind after the close point; and, before it, that
// the chain head is the one the facts were read at. Failing any of them inserts nothing.
func (l *FinishLedger) insertCommit(ctx context.Context, tx *sql.Tx, v *finishView) error {
	cond := `NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'finish.park' AND seq > ?)
AND NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND seq > ? AND ` + workAdmittingSQL + `)`
	err := l.insert(ctx, tx, v, kindFinishCommit, commitRow{Round: v.facts.Round.N}, cond, func(v *finishView) []any {
		return []any{l.profile, l.generation, v.facts.Round.FreezeSeq, l.profile, l.generation, v.closeSeq}
	})
	if errors.Is(err, ErrFinishMoved) {
		return fmt.Errorf("%w: the chain or the run moved", finish.ErrCommitRefused)
	}
	return err
}

// Park is the finish-park transition. The verdict must be a Park with a reason from the closed list; the run must be
// verifying. A park already recorded for the round is a no-op; a finished run refuses.
func (l *FinishLedger) Park(ctx context.Context, verdict finish.Verdict) error {
	if verdict.Outcome != finish.Park || !slices.Contains(parkReasons, verdict.Reason) {
		return fmt.Errorf("%w: a park needs a reason from the closed list", ErrFinishRow)
	}
	tx, err := l.s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := l.s.loadFinishView(ctx, tx, l.profile, l.generation)
	if err != nil {
		return err
	}
	switch v.facts.State {
	case finish.StateParked:
		return nil
	case finish.StateVerifying:
	default:
		return fmt.Errorf("%w: it is %s", ErrFinishState, v.facts.State)
	}
	detail := verdict.Detail
	if len(detail) > 256 {
		detail = detail[:256]
	}
	cond := `NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'finish.park' AND seq > ?)`
	if err := l.insert(ctx, tx, v, kindFinishPark, parkRow{Round: v.facts.Round.N, Reason: string(verdict.Reason), Detail: detail}, cond,
		func(v *finishView) []any { return []any{l.profile, l.generation, v.facts.Round.FreezeSeq} }); err != nil {
		return err
	}
	return tx.Commit()
}
