package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// Typed approvals as rows of the chained audit stream (note 13.4 section 3): an approval is an `approval.issue` row, a
// rescind is an `approval.rescind` row, a consume is the counter delta keyed by the approval id plus the operation's
// audit row. All three are MAC-chained and insert-only, so no new table is needed, and a reserved kind keeps anything
// but the typed writers below from producing them.

// Approval errors.
var (
	ErrApprovalUnknown  = errors.New("doomsday: no such approval")
	ErrApprovalRevoked  = errors.New("doomsday: the approval was rescinded")
	ErrApprovalExpired  = errors.New("doomsday: the approval expired")
	ErrApprovalMismatch = errors.New("doomsday: the approval is for another operation or effect")
)

// Expiry of an approval is counted in audit transitions, never in time (note 13.4).
const (
	DefaultApprovalTTLSeq = 100
	MaxApprovalTTLSeq     = 10000
)

const (
	kindApprovalIssue   = "approval.issue"
	kindApprovalRescind = "approval.rescind"
	approvalsKey        = "approvals" // the counter kind every approval consume charges once; see ConsumeApproval
)

// Approval is one issued approval as read back (MAC-verified, with its rescind state).
type Approval struct {
	ID          string
	Profile     string
	Generation  int64
	CharterID   string
	CharterHash string // hex
	Op          string
	ArgsHash    string
	Caller      int64
	IssuedSeq   int64
	ExpiresSeq  int64
	Boss        string
	Revoked     bool
}

// IssueInput is what the boss path supplies; the store fills the id, the charter id and the sequence numbers.
type IssueInput struct {
	Profile     string
	Generation  int64
	CharterHash string // hex of the charter hash the approval is for
	Op          string
	ArgsHash    string
	Caller      int64
	Boss        string
	TTLSeq      int64 // 0: DefaultApprovalTTLSeq
}

type approvalPayload struct {
	ID          string `json:"id"`
	Profile     string `json:"profile"`
	Generation  int64  `json:"generation"`
	CharterID   string `json:"charter_id"`
	CharterHash string `json:"charter_hash"`
	Op          string `json:"op"`
	ArgsHash    string `json:"args_hash"`
	Caller      int64  `json:"caller"`
	IssuedSeq   int64  `json:"issued_seq"`
	ExpiresSeq  int64  `json:"expires_seq"`
	Boss        string `json:"boss"`
	Nonce       string `json:"nonce"`
}

// ApprovalStore is the approval surface; SQLStore satisfies it. It is separate from Store so existing fakes stay valid.
type ApprovalStore interface {
	IssueApproval(ctx context.Context, in IssueInput) (Approval, error)
	RescindApproval(ctx context.Context, profile string, generation int64, id, by string) error
	GetApproval(ctx context.Context, id string) (Approval, error)
	// ApprovalSeq is the audit head of the profile's latest generation, which expires_seq is compared with.
	ApprovalSeq(ctx context.Context, profile string) (int64, error)
	// ConsumeApproval is ConsumeMany for a typed approval: the same statement that spends it checks it exists for this
	// operation and effect, is not rescinded and has not expired.
	ConsumeApproval(ctx context.Context, profile string, generation int64, approvalID, op, argsHash string, charges []Charge, audit AuditEntry) error
}

var _ ApprovalStore = (*SQLStore)(nil)

// IssueApproval records an approval in one conditional insert: the run must be the profile's running one and the charter
// hash must be the stored charter's. The row's own seq is issued_seq; expires_seq is issued_seq plus the TTL.
func (s *SQLStore) IssueApproval(ctx context.Context, in IssueInput) (Approval, error) {
	ttl := in.TTLSeq
	if ttl == 0 {
		ttl = DefaultApprovalTTLSeq
	}
	if in.Op == "" || in.ArgsHash == "" || in.Caller <= 0 || ttl < 1 || ttl > MaxApprovalTTLSeq {
		return Approval{}, fmt.Errorf("%w: operation, args hash, caller and a ttl of 1..%d are needed", ErrApprovalMismatch, MaxApprovalTTLSeq)
	}
	// BEGIN IMMEDIATE: the run, the charter and the chain head are read before the row is written, and a writer that
	// commits in between would make the write fail at once with SQLITE_BUSY_SNAPSHOT (#1460).
	var p approvalPayload
	err := dbconn.WithWriteTx(ctx, s.db, func(tx *sql.Conn) error {
		if err := s.Admit(ctx, tx, in.Profile, in.Generation); err != nil {
			return err
		}
		c, err := loadCharterRow(ctx, tx, s, in.Profile, in.Generation)
		if err != nil {
			return err
		}
		h := c.Hash()
		if hex.EncodeToString(h[:]) != in.CharterHash {
			return ErrHashMismatch
		}
		var head int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile = ? AND generation = ?`, in.Profile, in.Generation).Scan(&head); err != nil {
			return err
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		p = approvalPayload{ID: "ap-" + hex.EncodeToString(nonce[:8]), Profile: in.Profile, Generation: in.Generation, CharterID: c.ID(), CharterHash: in.CharterHash,
			Op: in.Op, ArgsHash: in.ArgsHash, Caller: in.Caller, IssuedSeq: head + 1, ExpiresSeq: head + 1 + ttl, Boss: in.Boss, Nonce: hex.EncodeToString(nonce[8:])}
		body, _ := json.Marshal(p)
		_, ok, err := s.appendAuditIf(ctx, tx, in.Profile, in.Generation, kindApprovalIssue, string(body), head,
			`EXISTS (SELECT 1 FROM doomsday_runs WHERE profile = ? AND generation = ? AND state = 'running')`, in.Profile, in.Generation)
		if err != nil {
			return err
		}
		if !ok {
			return &Refusal{Reason: "the run is not running or the chain moved"}
		}
		return nil
	})
	if err != nil {
		return Approval{}, err
	}
	return approvalFrom(p, false), nil
}

func approvalFrom(p approvalPayload, revoked bool) Approval {
	return Approval{ID: p.ID, Profile: p.Profile, Generation: p.Generation, CharterID: p.CharterID, CharterHash: p.CharterHash, Op: p.Op,
		ArgsHash: p.ArgsHash, Caller: p.Caller, IssuedSeq: p.IssuedSeq, ExpiresSeq: p.ExpiresSeq, Boss: p.Boss, Revoked: revoked}
}

const issueRow = `kind = 'approval.issue' AND json_extract(payload, '$.id') = ?`

// GetApproval reads an approval by id after verifying the whole chain of its generation: a chain that does not verify is
// ErrUnverifiable, never "no such approval".
func (s *SQLStore) GetApproval(ctx context.Context, id string) (Approval, error) {
	var profile, body string
	var generation, seq int64
	err := s.db.QueryRowContext(ctx, `SELECT profile, generation, seq, payload FROM doomsday_audit WHERE `+issueRow, id).Scan(&profile, &generation, &seq, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return Approval{}, ErrApprovalUnknown
	} else if err != nil {
		return Approval{}, err
	}
	if err := s.VerifyAudit(ctx, profile, generation); err != nil {
		return Approval{}, err
	}
	var p approvalPayload
	if json.Unmarshal([]byte(body), &p) != nil || p.ID != id || p.Profile != profile || p.Generation != generation || p.IssuedSeq != seq {
		return Approval{}, ErrUnverifiable
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'approval.rescind' AND json_extract(payload, '$.id') = ?`,
		profile, generation, id).Scan(&n); err != nil {
		return Approval{}, err
	}
	return approvalFrom(p, n > 0), nil
}

// ApprovalSeq is the head seq of the profile's highest generation (0 when it has none).
func (s *SQLStore) ApprovalSeq(ctx context.Context, profile string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile = ? AND generation = (SELECT COALESCE(MAX(generation),0) FROM doomsday_audit WHERE profile = ?)`,
		profile, profile).Scan(&seq)
	return seq, err
}

// RescindApproval writes the rescind row if the approval exists in this run, is not yet rescinded and is not consumed.
// The consumed test is in the same statement as the insert, so a rescind and a consume race in one place.
func (s *SQLStore) RescindApproval(ctx context.Context, profile string, generation int64, id, by string) error {
	body, _ := json.Marshal(map[string]string{"id": id, "by": by})
	cond := `EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND ` + issueRow + `)
AND NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'approval.rescind' AND json_extract(payload, '$.id') = ?)
AND NOT EXISTS (SELECT 1 FROM doomsday_counter_deltas WHERE approval_id = ?)`
	// BEGIN IMMEDIATE: the chain head is read before the row is written (#1460).
	return dbconn.WithWriteTx(ctx, s.db, func(tx *sql.Conn) error {
		_, ok, err := s.appendAuditIf(ctx, tx, profile, generation, kindApprovalRescind, string(body), 0, cond,
			profile, generation, id, profile, generation, id, id+"/"+approvalsKey)
		if err != nil {
			return err
		}
		if !ok {
			return s.classifyRescind(ctx, tx, profile, generation, id)
		}
		return nil
	})
}

func (s *SQLStore) classifyRescind(ctx context.Context, tx DBTX, profile string, generation int64, id string) error {
	var issued, rescinded, consumed int
	q := func(dst *int, sqlText string, a ...any) { _ = tx.QueryRowContext(ctx, sqlText, a...).Scan(dst) }
	q(&issued, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND `+issueRow, profile, generation, id)
	q(&rescinded, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'approval.rescind' AND json_extract(payload, '$.id') = ?`, profile, generation, id)
	q(&consumed, `SELECT COUNT(*) FROM doomsday_counter_deltas WHERE approval_id = ?`, id+"/"+approvalsKey)
	switch {
	case issued == 0:
		return ErrApprovalUnknown
	case rescinded > 0:
		return ErrApprovalRevoked
	case consumed > 0:
		return ErrAlreadyConsumed
	}
	return fmt.Errorf("doomsday: the rescind was not recorded")
}

// ConsumeApproval spends a typed approval. The first statement (the approval unit, charged once under the approval's id)
// is the conditional insert that also requires, in the same statement, that the issue row exists for this operation and
// effect, that no rescind row exists and that the chain head has not passed expires_seq; the cumulative kinds follow in
// the same transaction, then the audit row. Any failure leaves every counter as it was. A repeat is ErrAlreadyConsumed.
func (s *SQLStore) ConsumeApproval(ctx context.Context, profile string, generation int64, approvalID, op, argsHash string, charges []Charge, audit AuditEntry) error {
	if reservedKind(audit.Kind) {
		return ErrReservedKind
	}
	seen := map[string]bool{approvalsKey: true}
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
	valid := `EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND ` + issueRow + ` AND json_extract(payload, '$.op') = ? AND json_extract(payload, '$.args_hash') = ?
  AND json_extract(payload, '$.expires_seq') >= (SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile = ? AND generation = ?))
AND NOT EXISTS (SELECT 1 FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'approval.rescind' AND json_extract(payload, '$.id') = ?)`
	err = s.chargeTxIf(ctx, tx, profile, generation, approvalsKey, approvalID+"/"+approvalsKey, 1, 1<<62, valid,
		profile, generation, approvalID, op, argsHash, profile, generation, profile, generation, approvalID)
	if errors.Is(err, ErrOverLimit) { // the unit's own cap is unreachable: the approval was not valid
		err = s.classifyConsume(ctx, tx, profile, generation, approvalID, op, argsHash)
	}
	if err != nil {
		return err
	}
	var over []string
	for _, c := range charges {
		switch err := s.chargeTx(ctx, tx, profile, generation, c.Kind, approvalID+"/"+c.Kind, c.N, c.Limit); {
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

func (s *SQLStore) classifyConsume(ctx context.Context, tx DBTX, profile string, generation int64, id, op, argsHash string) error {
	var body string
	err := tx.QueryRowContext(ctx, `SELECT payload FROM doomsday_audit WHERE profile = ? AND generation = ? AND `+issueRow, profile, generation, id).Scan(&body)
	if err != nil {
		return ErrApprovalUnknown
	}
	var p approvalPayload
	if json.Unmarshal([]byte(body), &p) != nil {
		return ErrUnverifiable
	}
	var revoked, head, spent int64
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_counter_deltas WHERE approval_id = ?`, id+"/"+approvalsKey).Scan(&spent)
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM doomsday_audit WHERE profile = ? AND generation = ? AND kind = 'approval.rescind' AND json_extract(payload, '$.id') = ?`, profile, generation, id).Scan(&revoked)
	_ = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM doomsday_audit WHERE profile = ? AND generation = ?`, profile, generation).Scan(&head)
	switch {
	case spent > 0:
		return ErrAlreadyConsumed
	case revoked > 0:
		return ErrApprovalRevoked
	case head > p.ExpiresSeq:
		return ErrApprovalExpired
	case p.Op != op || p.ArgsHash != argsHash:
		return ErrApprovalMismatch
	}
	return ErrOverLimit
}
