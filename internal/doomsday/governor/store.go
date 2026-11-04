package governor

import (
	"context"
	"database/sql"
)

// Table names of migration 155. Only the governor sub-packages (slots, queue, state) write them; the migration's
// triggers hold the caps and the forward-only rules under any other writer.
const (
	TableSlots = "doomsday_slots"
	TableQueue = "doomsday_queue"
	TableState = "doomsday_governor_state"
)

// MACDomainQueue is the domain a queue row's MAC uses with the store's Keyer (same key path as the B1 rows), over
// profile, generation, qid, lead, class, bytes, payload_sha, state and enq_at, so a copied or restored row is detected.
const MACDomainQueue = "governor.queue"

// Keyer derives MACs; it is the same shape as the doomsday store's Keyer so the one implementation serves both.
type Keyer interface {
	MAC(domain string, parts ...[]byte) ([]byte, error)
}

// DB is the subset of *sql.DB and *sql.Tx the stores use. Slot reservation and queue admission need a real
// transaction (BEGIN IMMEDIATE semantics through the connection's busy handling), so the stores take a *sql.DB and
// open their own transactions with BeginTx; this interface is for read helpers.
type DB interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// SlotRow is one row of doomsday_slots.
type SlotRow struct {
	ID         int64
	Profile    string
	Generation int64
	Kind       Kind
	Tree       string
	MemberID   string
	CreatedAt  int64
	ReleasedAt int64 // 0 while live
}

// QueueState is the state of a queue row; it moves forward once, from Queued.
type QueueState string

const (
	QueueQueued    QueueState = "queued"
	QueueAdmitted  QueueState = "admitted"
	QueueCancelled QueueState = "cancelled"
)

// QueueRow is one row of doomsday_queue. A terminal row's Payload is empty; PayloadSHA keeps it bound to the MAC.
type QueueRow struct {
	Item
	Profile    string
	Generation int64
	Bytes      int64
	PayloadSHA [32]byte
	State      QueueState
	MAC        []byte
}

// StateStore holds derived governor state (backoff windows, recovery counters). It is not configuration: losing it
// restarts the governor at the safe minimum, and no value in it can raise a cap.
type StateStore interface {
	Get(ctx context.Context, profile string, generation int64, key string) (value string, ok bool, err error)
	Put(ctx context.Context, profile string, generation int64, key, value string) error
}

// RunView is the one thing the governor stores need from the doomsday store: whether the run is running and which
// members are live (for orphan reconciliation). The doomsday store satisfies it through an adapter in the integrator.
type RunView interface {
	Running(ctx context.Context, profile string, generation int64) (bool, error)
	LiveMemberIDs(ctx context.Context, profile string, generation int64) (map[string]bool, error)
}
