package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Audit kinds written only by this package's own methods (approvals, the finish and the strategy layer's lines). The generic AppendAudit and the
// audit kind a ConsumeMany caller names refuse them, so a holder of the store cannot forge an approval, a rescind or a
// finish row by appending a row of that kind.
var reservedKindPrefixes = []string{kindApprovalIssue, kindApprovalRescind, "finish.", StrategyKindPrefix}

// ErrReservedKind: the audit kind belongs to this package's typed writers.
var ErrReservedKind = errors.New("doomsday: that audit kind is written only by its typed writer")

func reservedKind(kind string) bool {
	for _, p := range reservedKindPrefixes {
		if strings.HasPrefix(kind, p) {
			return true
		}
	}
	return false
}

// workAdmittingSQL selects the audit rows that admit work: a counter consume, an approval issue or consume, and the
// approved row of any typed operation. The finish-commit's NOT EXISTS reads exactly this set after the close point.
const workAdmittingSQL = `(kind IN ('counter.consume','approval.issue','approval.consume') OR (kind LIKE 'ops.%' AND kind LIKE '%.approved'))`

// WorkAdmittingKind is workAdmittingSQL in Go, so a test can pin that the two agree.
func WorkAdmittingKind(kind string) bool {
	switch kind {
	case "counter.consume", "approval.issue", "approval.consume":
		return true
	}
	return strings.HasPrefix(kind, "ops.") && strings.HasSuffix(kind, ".approved")
}

// appendAuditIf appends one chained row only if cond (a SQL boolean over the same statement's database, with args) holds:
// the row is `INSERT ... SELECT ... WHERE cond`, so the check and the insert are one statement. It reports false when the
// condition did not hold and nothing was written. head, when positive, is the seq the caller read as the chain's head:
// the row is written only while that is still the head, so a decision made on facts read in this transaction cannot be
// inserted over a chain that has moved.
func (s *SQLStore) appendAuditIf(ctx context.Context, tx *sql.Tx, profile string, generation int64, kind, payload string, head int64, cond string, args ...any) (AuditRow, bool, error) {
	var seq int64
	var prev []byte
	err := tx.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`, profile, generation).Scan(&seq, &prev)
	if errors.Is(err, sql.ErrNoRows) {
		seq, prev = 0, nil
	} else if err != nil {
		return AuditRow{}, false, err
	}
	if head > 0 && seq != head {
		return AuditRow{}, false, nil
	}
	seq++
	mac, err := s.auditMAC(profile, generation, seq, prev, kind, payload)
	if err != nil {
		return AuditRow{}, false, err
	}
	if cond == "" {
		cond = "1"
	}
	q := `INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) SELECT ?,?,?,?,?,?,? WHERE ` + cond
	all := append([]any{profile, generation, seq, kind, payload, nonNil(prev), mac}, args...)
	res, err := tx.ExecContext(ctx, q, all...)
	if err != nil {
		return AuditRow{}, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return AuditRow{}, false, nil
	}
	return AuditRow{Profile: profile, Generation: generation, Seq: seq, Kind: kind, Payload: payload}, true, nil
}
