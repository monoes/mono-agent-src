package store

import (
	"context"
	"errors"
	"strings"
)

// StrategyKindPrefix is the reserved audit kind prefix of the strategy layer. Only AppendStrategyAuditTx writes it: the
// generic AppendAudit and the audit kind a ConsumeMany caller names refuse it (ErrReservedKind).
const StrategyKindPrefix = "strategy."

// ErrNotStrategyKind: AppendStrategyAuditTx writes strategy kinds and nothing else.
var ErrNotStrategyKind = errors.New("doomsday: the strategy writer appends strategy.* kinds only")

// AppendStrategyAuditTx appends one strategy.* row to the generation's MAC chain inside the CALLER's transaction, so a
// strategy row and its audit line commit or roll back together. It is the same chain, MAC scheme and domain as AppendAudit
// and writes no other kind. It does not sequence the write: a caller in a running host runs it inside the witnessed
// sequence (stop.Sequencer), exactly as the SeqStore does for the other writers, or the next admission fails closed.
func (s *SQLStore) AppendStrategyAuditTx(ctx context.Context, tx DBTX, profile string, generation int64, kind, payload string) error {
	if !strings.HasPrefix(kind, StrategyKindPrefix) || len(kind) == len(StrategyKindPrefix) {
		return ErrNotStrategyKind
	}
	_, err := s.appendAuditTx(ctx, tx, profile, generation, kind, payload)
	return err
}
