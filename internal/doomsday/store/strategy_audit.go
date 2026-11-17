package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/monoes/mono-agent/internal/doomsday/strategy/types"
)

// StrategyKindPrefix is the reserved audit kind prefix of the strategy layer. Only AppendStrategyAuditTx writes it: the
// generic AppendAudit and the audit kind a ConsumeMany caller names refuse it (ErrReservedKind).
const StrategyKindPrefix = "strategy."

// ErrNotStrategyKind: AppendStrategyAuditTx writes strategy kinds and nothing else.
var ErrNotStrategyKind = errors.New("doomsday: the strategy writer appends strategy.* kinds only")

// StrategyBadReasons returns the reason recorded on each strategy.bad-judgment line of the generation, by epoch id. It
// reads through the verifying chain reader, so a line that fails its MAC fails the whole read. Lines written before the
// reason existed carry none and are absent from the map.
func (s *SQLStore) StrategyBadReasons(ctx context.Context, profile string, generation int64) (map[string]string, error) {
	rows, err := s.readChain(ctx, s.db, profile, generation)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rows {
		if r.Kind != types.KindBadJudgment {
			continue
		}
		var p struct{ Epoch, Reason string }
		if json.Unmarshal([]byte(r.Payload), &p) == nil && p.Reason != "" {
			out[p.Epoch] = p.Reason
		}
	}
	return out, nil
}

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
