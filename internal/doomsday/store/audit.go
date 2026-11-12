package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"

	"github.com/monoes/mono-agent/internal/dbconn"
)

func (s *SQLStore) auditMAC(profile string, generation, seq int64, prev []byte, kind, payload string) ([]byte, error) {
	return s.keys.MAC("audit", b(profile), i64(generation), i64(seq), prev, b(kind), b(payload))
}

// AppendAudit appends one row to the generation's MAC chain. The row carries the previous row's MAC, so removing,
// reordering or replacing a row breaks the chain; (profile, generation, seq) is unique, so two writers cannot both
// extend the same head.
func (s *SQLStore) AppendAudit(ctx context.Context, profile string, generation int64, kind, payload string) (AuditRow, error) {
	if reservedKind(kind) {
		return AuditRow{}, ErrReservedKind
	}
	// BEGIN IMMEDIATE: the chain head is read before the row is written, and a writer that commits in between would
	// make the write fail at once with SQLITE_BUSY_SNAPSHOT, which busy_timeout does not wait for (#1460).
	var row AuditRow
	err := dbconn.WithWriteTx(ctx, s.db, func(tx *sql.Conn) error {
		var err error
		row, err = s.appendAuditTx(ctx, tx, profile, generation, kind, payload)
		return err
	})
	if err != nil {
		return AuditRow{}, err
	}
	return row, nil
}

func (s *SQLStore) appendAuditTx(ctx context.Context, q DBTX, profile string, generation int64, kind, payload string) (AuditRow, error) {
	var seq int64
	var prev []byte
	err := q.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`, profile, generation).Scan(&seq, &prev)
	if errors.Is(err, sql.ErrNoRows) {
		seq, prev = 0, nil
	} else if err != nil {
		return AuditRow{}, err
	}
	seq++
	mac, err := s.auditMAC(profile, generation, seq, prev, kind, payload)
	if err != nil {
		return AuditRow{}, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO doomsday_audit(profile, generation, seq, kind, payload, prev, mac) VALUES (?,?,?,?,?,?,?)`,
		profile, generation, seq, kind, payload, nonNil(prev), mac); err != nil {
		return AuditRow{}, err
	}
	return AuditRow{Profile: profile, Generation: generation, Seq: seq, Kind: kind, Payload: payload}, nil
}

func nonNil(p []byte) []byte {
	if p == nil {
		return []byte{}
	}
	return p
}

// querier is what reading the chain needs; *sql.DB and *sql.Tx both satisfy it.
type querier interface {
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// chainRow is one verified audit row.
type chainRow struct {
	Seq           int64
	Kind, Payload string
}

// VerifyAudit walks the chain from seq 1: contiguous seq, each prev equal to the previous MAC, each MAC valid.
// A removed tail is not visible here; the witness of the stop package is what catches truncation.
func (s *SQLStore) VerifyAudit(ctx context.Context, profile string, generation int64) error {
	_, err := s.readChain(ctx, s.db, profile, generation)
	return err
}

// readChain verifies the whole chain of a generation through q and returns its rows in order. It is the one reader of
// the chain, so a decision made on the rows (the finish) is made on rows whose MACs were just checked.
func (s *SQLStore) readChain(ctx context.Context, q querier, profile string, generation int64) ([]chainRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT seq, kind, payload, prev, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq`, profile, generation)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := int64(1)
	var prev []byte
	var out []chainRow
	for rows.Next() {
		var seq int64
		var kind, payload string
		var p, mc []byte
		if err := rows.Scan(&seq, &kind, &payload, &p, &mc); err != nil {
			return nil, err
		}
		if seq != want || !bytes.Equal(p, nonNil(prev)) {
			return nil, ErrUnverifiable
		}
		exp, err := s.auditMAC(profile, generation, seq, prev, kind, payload)
		if err != nil || !hmac.Equal(exp, mc) {
			return nil, ErrUnverifiable
		}
		out = append(out, chainRow{seq, kind, payload})
		prev, want = mc, want+1
	}
	return out, rows.Err()
}
