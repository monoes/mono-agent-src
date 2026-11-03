package store

import (
	"context"
	"errors"
	"strings"
)

// Charge is one counter kind to add to: n units against limit (the cap of the cumulative total).
type Charge struct {
	Kind  string
	N     int64
	Limit int64
}

// AuditEntry is the audit row written in the same transaction as the charges.
type AuditEntry struct {
	Kind    string
	Payload string
}

// OverLimitError names every kind whose cumulative cap the charge would have crossed. errors.Is(err, ErrOverLimit) holds.
type OverLimitError struct{ Kinds []string }

func (e *OverLimitError) Error() string {
	return ErrOverLimit.Error() + ": " + strings.Join(e.Kinds, ",")
}
func (e *OverLimitError) Is(target error) bool { return target == ErrOverLimit }

// MultiConsumer is the atomic multi-kind consume. It is separate from Store so existing fakes of Store stay valid.
type MultiConsumer interface {
	ConsumeMany(ctx context.Context, profile string, generation int64, approvalID string, charges []Charge, audit AuditEntry) error
}

var _ MultiConsumer = (*SQLStore)(nil)

// ConsumeMany charges every kind and appends the audit row in ONE transaction: either all charges and the audit row are
// recorded, or none is (an over-limit kind, a replay, a stopped run or a failed audit append leaves every counter as it
// was). Each kind is its own conditional insert, keyed approvalID+"/"+kind, so a retry of the same approval is
// ErrAlreadyConsumed and is never double counted. All over-limit kinds are reported together in an *OverLimitError.
// There must be at least one charge, kinds must be distinct, and every n must be positive.
func (s *SQLStore) ConsumeMany(ctx context.Context, profile string, generation int64, approvalID string, charges []Charge, audit AuditEntry) error {
	if len(charges) == 0 {
		return ErrOverLimit
	}
	if reservedKind(audit.Kind) {
		return ErrReservedKind
	}
	seen := map[string]bool{}
	for _, c := range charges {
		if c.N <= 0 || c.Limit < 0 || c.Kind == "" || seen[c.Kind] {
			return ErrOverLimit
		}
		seen[c.Kind] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var over []string
	for _, c := range charges {
		err := s.chargeTx(ctx, tx, profile, generation, c.Kind, approvalID+"/"+c.Kind, c.N, c.Limit)
		switch {
		case err == nil:
		case errors.Is(err, ErrOverLimit):
			over = append(over, c.Kind)
		default:
			return err
		}
	}
	if len(over) > 0 {
		return &OverLimitError{Kinds: over}
	}
	if _, err := s.appendAuditTx(ctx, tx, profile, generation, audit.Kind, audit.Payload); err != nil {
		return err
	}
	return tx.Commit()
}
