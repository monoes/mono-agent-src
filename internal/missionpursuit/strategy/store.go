package strategy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// sentinel is an error that also answers to the sstore sentinel it stands in for (matched by its text), so that the epoch
// engine and the policy intake, which match sstore's errors, treat this store's errors the same way. A test pins each pair.
type sentinel struct{ msg, standsFor string }

func (e sentinel) Error() string { return e.msg }
func (e sentinel) Is(target error) bool {
	return target != nil && target.Error() == e.standsFor
}

// Typed errors. None carries row content.
var (
	ErrNotRunning     error = sentinel{"missionstrategy: the session is not running", "sstore: the run is not running"}
	ErrNeedsOpenEpoch error = sentinel{"missionstrategy: the epoch is not open", "sstore: the epoch is not open"}
	ErrBackwardMove   error = sentinel{"missionstrategy: state moves forward only", "sstore: state moves forward only"}
	ErrNotFound       error = sentinel{"missionstrategy: no such row", "sstore: no such row"}
	ErrConflict       error = sentinel{"missionstrategy: a row with that identity exists with different content", "sstore: a row with that identity exists with different content"}
	ErrInvalid        error = sentinel{"missionstrategy: invalid record", "sstore: invalid record"}
	ErrUncheckedLimit error = sentinel{"missionstrategy: too many unchecked proposals in this epoch", "sstore: too many unchecked proposals in this epoch"}
)

// DuplicateKeyError: a direction with the same key hash exists in the session. ExistingID names it. (The policy intake
// matches *sstore.DuplicateKeyError, which this package cannot import: the wiring translates it.)
type DuplicateKeyError struct{ ExistingID string }

func (e *DuplicateKeyError) Error() string {
	return "missionstrategy: a direction with that key already exists in this session"
}

// Store is the accessor set over the mission_strategy_* tables.
type Store struct{ db *sql.DB }

// NewStore builds a Store over db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

type queryer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// sessionOf maps the library's (profile, generation) to the task session: the profile is the session id in decimal, plain,
// and the generation is 1.
func sessionOf(profile string, gen int64) (int64, error) {
	id, err := strconv.ParseInt(profile, 10, 64)
	if gen != 1 || err != nil || id < 1 || strconv.FormatInt(id, 10) != profile {
		return 0, fmt.Errorf("%w: a mission strategy run is (decimal session id, generation 1)", ErrInvalid)
	}
	return id, nil
}

// tx runs fn in a BEGIN IMMEDIATE transaction on a connection of its own, so that what fn reads (the session state, the
// epoch) holds until the commit. An error from fn rolls back.
func (s *Store) tx(ctx context.Context, fn func(x queryer) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("missionstrategy: connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		return fmt.Errorf("missionstrategy: begin: %w", err)
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
	if _, err := conn.ExecContext(context.WithoutCancel(ctx), "COMMIT"); err != nil {
		return fmt.Errorf("missionstrategy: commit: %w", err)
	}
	committed = true
	return nil
}

// requireRunning refuses unless the session is in state running. The triggers refuse inserts and moves of a session that
// is not running too; this check gives the typed error before the statement runs.
func requireRunning(ctx context.Context, q queryer, session int64) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM task_sessions WHERE id = ?`, session).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && state != "running") {
		return ErrNotRunning
	}
	return err
}

// Running tells whether the session is running (policy.RunState). A missing session is not running; an invalid pair is an error.
func (s *Store) Running(ctx context.Context, profile string, gen int64) (bool, error) {
	session, err := sessionOf(profile, gen)
	if err != nil {
		return false, err
	}
	err = requireRunning(ctx, s.db, session)
	if errors.Is(err, ErrNotRunning) {
		return false, nil
	}
	return err == nil, err
}

// SessionRun is the epoch engine's RunState for one session: it reads the task session's state at every call.
type SessionRun struct {
	store   *Store
	session int64
}

// RunOf is the epoch.RunState of the session.
func (s *Store) RunOf(session int64) SessionRun { return SessionRun{store: s, session: session} }

// Running tells whether the session is running.
func (r SessionRun) Running(ctx context.Context) (bool, error) {
	return r.store.Running(ctx, strconv.FormatInt(r.session, 10), 1)
}

// mapErr turns the migration's trigger messages into typed errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	m := err.Error()
	switch {
	case strings.Contains(m, "only for a running session"):
		return ErrNotRunning
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
		return h, fmt.Errorf("%w: a stored hash is not 32 bytes", ErrInvalid)
	}
	copy(h[:], b)
	return h, nil
}
