package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrRunsUnreadable is what OpenRuns reports when it could not even LIST the profile's non-quiescent runs: the table is
// missing, or the database is busy, locked, closed or corrupt. No run row was read, so nothing says one exists. It is a
// different thing from a row that exists and does not verify (ErrUnverifiable, or a key that cannot be read), which is
// never wrapped in this error: that one always means a run is there.
var ErrRunsUnreadable = errors.New("doomsday: the run table cannot be read")

// RunsTableMissing says err is ErrRunsUnreadable because doomsday_runs does not exist (no migration applied yet), as
// opposed to a table that is there and cannot be read. Both are unreadable; callers may tell them apart for display only.
func RunsTableMissing(err error) bool {
	return errors.Is(err, ErrRunsUnreadable) && strings.Contains(err.Error(), "no such table")
}

// AuditHead is the head of one generation's audit chain: the last seq and its MAC. An empty chain is seq 0.
type AuditHead struct {
	Seq int64
	MAC []byte
}

// RunLister and AuditHeadReader are separate from Store so that existing Store fakes keep compiling; SQLStore
// satisfies both.
type RunLister interface {
	// OpenRuns lists the profile's runs that are not quiescent, each after its MAC is verified.
	OpenRuns(ctx context.Context, profile string) ([]Run, error)
}

type AuditHeadReader interface {
	// AuditHead returns the head of the generation's chain after verifying the whole chain.
	AuditHead(ctx context.Context, profile string, generation int64) (AuditHead, error)
}

var (
	_ RunLister       = (*SQLStore)(nil)
	_ AuditHeadReader = (*SQLStore)(nil)
)

// OpenRuns lists the profile's non-quiescent runs, oldest first. A row that does not verify fails the whole call:
// an unverifiable run is never reported as a healthy or an absent one.
func (s *SQLStore) OpenRuns(ctx context.Context, profile string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT generation FROM doomsday_runs WHERE profile = ? AND state <> 'quiescent' ORDER BY generation`, profile)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRunsUnreadable, err) // nothing was listed: this says no run exists, and no run does not exist
	}
	var gens []int64
	for rows.Next() {
		var g int64
		if err := rows.Scan(&g); err != nil {
			rows.Close()
			return nil, err
		}
		gens = append(gens, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		if len(gens) == 0 { // the listing failed before it named any run
			return nil, fmt.Errorf("%w: %w", ErrRunsUnreadable, err)
		}
		return nil, err
	}
	rows.Close()
	var out []Run
	for _, g := range gens {
		r, err := s.GetRun(ctx, profile, g)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// AuditHead verifies the chain, then returns its last row's seq and MAC.
func (s *SQLStore) AuditHead(ctx context.Context, profile string, generation int64) (AuditHead, error) {
	if err := s.VerifyAudit(ctx, profile, generation); err != nil {
		return AuditHead{}, err
	}
	var h AuditHead
	err := s.db.QueryRowContext(ctx, `SELECT seq, mac FROM doomsday_audit WHERE profile = ? AND generation = ? ORDER BY seq DESC LIMIT 1`, profile, generation).Scan(&h.Seq, &h.MAC)
	if errors.Is(err, sql.ErrNoRows) {
		return AuditHead{}, nil
	}
	return h, err
}
