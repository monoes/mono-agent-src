package applypark

import (
	"context"
	"database/sql"
	"os"
	"syscall"
	"time"

	"github.com/monoes/mono-agent/internal/dbconn"
)

// Slots is the profile-wide reservation behind the queue's cap. A fill in
// flight holds a row in application_slots; a parked tab holds a slot through
// application_tabs. Reserve counts both and inserts in one immediate
// transaction, so two queue processes share one cap. A row is leased
// (renewed by its owner) and names the owner's pid: a row whose lease ran out
// or whose process is gone is removed before the count.
type Slots struct {
	DB      *sql.DB
	Profile string
	// Owner and Pid identify the queue process; Owner must be unique per queue.
	Owner string
	Pid   int
	// Lease is how long a reservation lives without a renewal (default 2 min).
	Lease time.Duration
	// Alive reports whether a pid runs (default: signal 0). A var for tests.
	Alive func(pid int) bool
	// Now is the clock (default time.Now).
	Now func() time.Time
}

func (s Slots) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Slots) lease() time.Duration {
	if s.Lease > 0 {
		return s.Lease
	}
	return 2 * time.Minute
}

func (s Slots) alive(pid int) bool {
	if s.Alive != nil {
		return s.Alive(pid)
	}
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}

// Reserve takes a slot for appID if fewer than max are in use, counting live
// reservations plus parked or active tabs of applications still waiting on the
// person. It reports false when the cap is reached or another queue holds
// appID.
func (s Slots) Reserve(ctx context.Context, appID string, max int) (ok bool, err error) {
	conn, err := s.DB.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		dbconn.AbandonBegin(conn)
		return false, err
	}
	defer func() {
		if err != nil || !ok {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			return
		}
		if _, cerr := conn.ExecContext(ctx, `COMMIT`); cerr != nil {
			ok, err = false, cerr
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if err = s.expire(ctx, conn); err != nil {
		return false, err
	}
	var holder string
	switch qerr := conn.QueryRowContext(ctx, `SELECT owner FROM application_slots WHERE application_id = ?`, appID).Scan(&holder); {
	case qerr == sql.ErrNoRows:
	case qerr != nil:
		return false, qerr
	case holder != s.Owner:
		return false, nil
	}
	var used int
	if err = conn.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM application_slots x JOIN applications a ON a.id = x.application_id
		          WHERE a.profile_id = ? AND x.application_id != ?)
		      + (SELECT COUNT(*) FROM application_tabs t JOIN applications a ON a.id = t.application_id
		          WHERE a.profile_id = ? AND t.application_id != ? AND t.state IN ('parked', 'active')
		            AND a.status IN ('awaiting_submit', 'needs_you')
		            AND t.application_id NOT IN (SELECT application_id FROM application_slots))`,
		s.Profile, appID, s.Profile, appID).Scan(&used); err != nil {
		return false, err
	}
	if used >= ClampMax(max) {
		return false, nil
	}
	_, err = conn.ExecContext(ctx,
		`INSERT INTO application_slots (application_id, owner, pid, lease_until, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(application_id) DO UPDATE SET lease_until = excluded.lease_until`,
		appID, s.Owner, s.Pid, s.now().Add(s.lease()).Unix(), now())
	return err == nil, err
}

// expire removes the reservations whose lease ran out or whose process is gone.
func (s Slots) expire(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `DELETE FROM application_slots WHERE lease_until < ?`, s.now().Unix()); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `SELECT DISTINCT pid FROM application_slots`)
	if err != nil {
		return err
	}
	var gone []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return err
		}
		if !s.alive(pid) {
			gone = append(gone, pid)
		}
	}
	rows.Close()
	for _, pid := range gone {
		if _, err := conn.ExecContext(ctx, `DELETE FROM application_slots WHERE pid = ?`, pid); err != nil {
			return err
		}
	}
	return nil
}

// Release gives appID's slot back (a no-op when this owner does not hold it).
func (s Slots) Release(ctx context.Context, appID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM application_slots WHERE application_id = ? AND owner = ?`, appID, s.Owner)
	return err
}

// Renew extends every reservation this owner holds.
func (s Slots) Renew(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE application_slots SET lease_until = ? WHERE owner = ?`,
		s.now().Add(s.lease()).Unix(), s.Owner)
	return err
}
