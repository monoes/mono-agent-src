// Package slots is the SQLite implementation of governor.Slots: process and session slot accounting for one
// open-ended ("doomsday") run. Every reservation is one BEGIN IMMEDIATE transaction on a dedicated connection, so the
// live count and the insert are atomic across goroutines and across processes sharing the database file. The
// migration's triggers hold the charter caps under any other writer; this package additionally applies the lower of
// the governor's effective limit and the charter cap. Nothing here starts a goroutine, registers itself or runs at
// import time.
package slots

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// Store implements governor.Slots.
type Store struct {
	db   *sql.DB
	view governor.RunView
	now  func() time.Time

	// afterProcessInsert, when set (tests only), runs inside a session reservation between the process-slot insert
	// and the session-slot insert; an error rolls the whole reservation back.
	afterProcessInsert func() error
}

var _ governor.Slots = (*Store)(nil)

// New builds the store. view supplies the live registry members for Reconcile.
func New(db *sql.DB, view governor.RunView) *Store {
	return &Store{db: db, view: view, now: time.Now}
}

// Variables so that tests can shorten them: only the BEGIN is retried on a busy database, nothing is written yet.
var (
	busyRetries = 3
	busyBackoff = 100 * time.Millisecond
)

// noTree is stored for a slot that has no tree (the root, a tool process outside a tree): the column is NOT NULL and
// non-empty.
const noTree = "-"

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own. A failed or cancelled BEGIN, and every
// error path after it, ends in ROLLBACK on the same connection, so the pooled connection never goes back inside a
// transaction (#1315).
func (s *Store) tx(ctx context.Context, fn func(c *sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("governor slots: connection: %w", err)
	}
	defer conn.Close()
	if err := beginImmediate(ctx, conn); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return fmt.Errorf("governor slots: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := fn(conn); err != nil {
		return err
	}
	// The work is done: a context that ends now must not report a commit that goes through as a failure.
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("governor slots: commit: %w", err)
	}
	committed = true
	return nil
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	for attempt := 0; ; attempt++ {
		_, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
		if err == nil || !isBusy(err) || attempt >= busyRetries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(busyBackoff << attempt):
		}
	}
}

func isBusy(err error) bool {
	m := err.Error()
	return strings.Contains(m, "SQLITE_BUSY") || strings.Contains(m, "database is locked") || strings.Contains(m, "database table is locked")
}

// Reserve takes the slots r needs, all or none. limit is the governor's effective concurrency for process slots; the
// charter cap l.MaxConcurrent lowers it further, never raises it. A limit under 1 admits nothing.
func (s *Store) Reserve(ctx context.Context, l governor.Limits, limit int, r Request) error {
	if err := validate(l, r); err != nil {
		return err
	}
	if limit > l.MaxConcurrent {
		limit = l.MaxConcurrent
	}
	tree := r.Tree
	if tree == "" {
		tree = noTree
	}
	return s.tx(ctx, func(c *sql.Conn) error {
		var state string
		err := c.QueryRowContext(ctx, `SELECT state FROM doomsday_runs WHERE profile = ? AND generation = ?`, l.Profile, l.Generation).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && state != "running") {
			return governor.ErrNotRunning
		}
		if err != nil {
			return err
		}
		var held int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM doomsday_slots WHERE profile = ? AND generation = ? AND member_id = ? AND released_at IS NULL`,
			l.Profile, l.Generation, r.MemberID).Scan(&held); err != nil {
			return err
		}
		if held > 0 {
			return governor.ErrAlreadyHeld
		}
		var procs int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM doomsday_slots WHERE profile = ? AND generation = ? AND kind = 'process' AND released_at IS NULL`,
			l.Profile, l.Generation).Scan(&procs); err != nil {
			return err
		}
		if procs >= limit {
			return governor.ErrNoSlot
		}
		if r.Kind == governor.KindSession {
			var sess int
			if err := c.QueryRowContext(ctx, `SELECT count(*) FROM doomsday_slots WHERE profile = ? AND generation = ? AND kind = 'session' AND tree = ? AND released_at IS NULL`,
				l.Profile, l.Generation, tree).Scan(&sess); err != nil {
				return err
			}
			if sess >= l.LivePerTree {
				return governor.ErrNoSlot
			}
		}
		if err := s.insert(ctx, c, l, governor.KindProcess, tree, r.MemberID); err != nil {
			return err
		}
		if r.Kind != governor.KindSession {
			return nil
		}
		if s.afterProcessInsert != nil {
			if err := s.afterProcessInsert(); err != nil {
				return err
			}
		}
		return s.insert(ctx, c, l, governor.KindSession, tree, r.MemberID)
	})
}

// Request is the governor package's request type, aliased so callers need one import.
type Request = governor.Request

func validate(l governor.Limits, r Request) error {
	switch {
	case r.MemberID == "":
		return errors.New("governor slots: member id required")
	case r.Kind != governor.KindSession && r.Kind != governor.KindProcess:
		return fmt.Errorf("governor slots: unknown slot kind %q", r.Kind)
	case r.Kind == governor.KindSession && r.Tree == "":
		return errors.New("governor slots: a session slot needs a tree")
	case l.Profile == "":
		return errors.New("governor slots: profile required")
	}
	return nil
}

// insert writes one live slot. The migration's triggers are the backstop: their refusals map to the typed errors.
func (s *Store) insert(ctx context.Context, c *sql.Conn, l governor.Limits, k governor.Kind, tree, member string) error {
	_, err := c.ExecContext(ctx, `INSERT INTO doomsday_slots(profile, generation, kind, tree, member_id, created_at) VALUES (?,?,?,?,?,?)`,
		l.Profile, l.Generation, string(k), tree, member, s.now().Unix())
	return mapTrigger(err)
}

func mapTrigger(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "max_concurrent reached"), strings.Contains(m, "live_per_tree reached"):
		return governor.ErrNoSlot
	case strings.Contains(m, "needs a running run"):
		return governor.ErrNotRunning
	case strings.Contains(m, "UNIQUE constraint"):
		return governor.ErrAlreadyHeld
	}
	return err
}

// Release frees every live slot of the member; a member without slots is not an error. It does not need the run to be
// running: a stopped run's slots are released all the same.
func (s *Store) Release(ctx context.Context, profile string, generation int64, memberID string) error {
	return s.tx(ctx, func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `UPDATE doomsday_slots SET released_at = ? WHERE profile = ? AND generation = ? AND member_id = ? AND released_at IS NULL`,
			s.now().Unix(), profile, generation, memberID)
		return err
	})
}

// Live counts live slots of a kind; tree "" counts every tree.
func (s *Store) Live(ctx context.Context, profile string, generation int64, k governor.Kind, tree string) (int, error) {
	q := `SELECT count(*) FROM doomsday_slots WHERE profile = ? AND generation = ? AND kind = ? AND released_at IS NULL`
	args := []any{profile, generation, string(k)}
	if tree != "" {
		q += ` AND tree = ?`
		args = append(args, tree)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
