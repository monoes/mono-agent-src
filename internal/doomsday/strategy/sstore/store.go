// Package sstore is the typed accessor layer over the strategy tables (migration 156): directions, epochs, judgments
// and the novelty ledger. It is a plain library: no init, no goroutine, no registration, nothing wired into a binary.
//
// Every row it writes carries a MAC over the types package's Canonical() bytes, computed through a narrow Keyer (the
// store package's HMACKeyer satisfies it, so the per-run key and the MAC scheme are the ones the charter and audit rows
// already use). Every write also appends its strategy.* audit line through an AuditSink in the SAME transaction, so a
// row never exists without its audit line and an audit failure rolls the row back. Every read verifies the MAC and the
// stored hashes; a row that fails is reported as ErrTampered, which the caller treats as "park the strategy, not the run".
package sstore

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Typed errors. Callers match with errors.Is / errors.As; none of them carries row content.
var (
	ErrRunNotRunning  = errors.New("sstore: the run is not running")
	ErrNeedsOpenEpoch = errors.New("sstore: the epoch is not open")
	ErrBackwardMove   = errors.New("sstore: state moves forward only")
	ErrNotFound       = errors.New("sstore: no such row")
	ErrTampered       = errors.New("sstore: a stored row failed verification")
	ErrConflict       = errors.New("sstore: a row with that identity exists with different content")
	ErrInvalid        = errors.New("sstore: invalid record")
	ErrUncheckedLimit = errors.New("sstore: too many unchecked proposals in this epoch")
	ErrAudit          = errors.New("sstore: audit line could not be appended")
	ErrKey            = errors.New("sstore: the MAC key is unavailable")
)

// DuplicateKeyError: a direction with the same key hash exists in the run. ExistingID names it, so a refusal can cite it.
type DuplicateKeyError struct{ ExistingID string }

func (e *DuplicateKeyError) Error() string {
	return "sstore: a direction with that key already exists in this run"
}

// Keyer derives MACs; it is the same shape as the store package's Keyer, so store.HMACKeyer satisfies it.
type Keyer interface {
	MAC(domain string, parts ...[]byte) ([]byte, error)
}

// Querier is the read surface; *sql.DB and *sql.Tx satisfy it.
type Querier interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// Tx is one transaction. A fake may implement it.
type Tx interface {
	Querier
	Commit() error
	Rollback() error
}

// DB begins transactions and answers reads.
type DB interface {
	Querier
	Begin(ctx context.Context) (Tx, error)
}

// WrapDB adapts a *sql.DB.
func WrapDB(db *sql.DB) DB { return sqlDB{db} }

type sqlDB struct{ *sql.DB }

func (d sqlDB) Begin(ctx context.Context) (Tx, error) { return d.DB.BeginTx(ctx, nil) }

// AuditSink appends one audit line inside the caller's transaction. The real one (the store package's chained writer,
// reached through the integrator's seam) must use tx, not its own connection, so the line commits or rolls back with the row.
type AuditSink interface {
	Append(ctx context.Context, tx Tx, profile string, generation int64, kind, payload string) error
}

// Store is the accessor set. Construct with New; the zero value is not usable.
type Store struct {
	db    DB
	keys  Keyer
	audit AuditSink
}

// New builds a Store over db with the MAC keyer and the audit sink.
func New(db DB, keys Keyer, audit AuditSink) *Store { return &Store{db: db, keys: keys, audit: audit} }

const macDomain = "strategy"

func (s *Store) mac(canonical []byte) ([]byte, error) {
	m, err := s.keys.MAC(macDomain, canonical)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrKey, err)
	}
	return m, nil
}

// verify compares a stored MAC with the one computed over the canonical bytes.
func (s *Store) verify(canonical, stored []byte) error {
	want, err := s.mac(canonical)
	if err != nil {
		return err
	}
	if !hmac.Equal(want, stored) {
		return ErrTampered
	}
	return nil
}

// inTx runs fn in one transaction: commit on nil, rollback otherwise.
func (s *Store) inTx(ctx context.Context, fn func(Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) appendAudit(ctx context.Context, tx Tx, profile string, gen int64, kind, payload string) error {
	if err := s.audit.Append(ctx, tx, profile, gen, kind, payload); err != nil {
		return fmt.Errorf("%w: %v", ErrAudit, err)
	}
	return nil
}

// requireRunning refuses unless the run is in state running. The database trigger refuses inserts too; moves are
// updates, which the trigger does not cover, so this is the check that closes them after a Stop.
func requireRunning(ctx context.Context, q Querier, profile string, gen int64) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM doomsday_runs WHERE profile = ? AND generation = ?`, profile, gen).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state != "running") {
		return ErrRunNotRunning
	}
	return err
}

// mapErr turns the migration's trigger and constraint messages into typed errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "only for a running run"):
		return ErrRunNotRunning
	case strings.Contains(m, "needs an open epoch"):
		return ErrNeedsOpenEpoch
	case strings.Contains(m, "moves forward only"):
		return ErrBackwardMove
	}
	return err
}

func h32(b []byte) ([32]byte, error) {
	var h [32]byte
	if len(b) != 32 {
		return h, ErrTampered
	}
	copy(h[:], b)
	return h, nil
}
