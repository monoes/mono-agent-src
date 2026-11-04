// Package queue is the bounded durable queue of pending launches of the open-ended ("doomsday") mode, over the
// doomsday_queue table of migration 155. The row and byte bounds live in the table's triggers (this package also
// refuses an oversized payload before it writes anything); every row carries a MAC over its canonical fields, which is
// verified on every read that leads to an admission, so a copied, restored or edited row is never admitted.
//
// Nothing here runs by itself: no init(), no goroutine, no registration. A caller builds a Queue and calls it.
package queue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monoes/mono-agent/internal/doomsday/governor"
)

// MaxLeadBytes bounds a lead name.
const MaxLeadBytes = 256

// MaxPeek is the most items one Peek returns, whatever the caller asks for.
const MaxPeek = governor.DefaultQueueMaxRows

// Refusals of this package besides the governor contract's.
var (
	// ErrInvalidItem is a refusal of an item before anything is written.
	ErrInvalidItem = errors.New("doomsday governor queue: invalid item")
	// ErrRowTampered says a queued row's MAC (or its payload hash) did not verify; the row has been cancelled.
	ErrRowTampered = errors.New("doomsday governor queue: the row does not verify and was cancelled")
)

var _ governor.Queue = (*Queue)(nil)

// Queue is the governor.Queue over SQLite.
type Queue struct {
	db   *sql.DB
	keys governor.Keyer
	now  func() time.Time

	// beforeCommit is a test seam: called inside the transaction just before COMMIT; an error rolls it back.
	beforeCommit func() error
}

// New builds a Queue over a migrated database. keys is the doomsday store's Keyer.
func New(db *sql.DB, keys governor.Keyer) *Queue {
	return &Queue{db: db, keys: keys, now: time.Now}
}

// Enqueue adds an item. Item.QID and Item.EnqAt are ignored: the queue assigns both. A full queue is ErrQueueFull, a
// run that is not running ErrNotRunning; a refusal writes nothing.
func (q *Queue) Enqueue(ctx context.Context, l governor.Limits, it governor.Item) (int64, error) {
	l = l.WithDefaults()
	if err := validate(l, it); err != nil {
		return 0, err
	}
	var qid int64
	err := q.tx(ctx, func(x governor.DB) error {
		var seq int64
		if err := x.QueryRowContext(ctx, `SELECT COALESCE((SELECT seq FROM sqlite_sequence WHERE name = 'doomsday_queue'), 0)`).Scan(&seq); err != nil {
			return err
		}
		// The qid is predictable inside the write transaction, and the MAC covers it, so the row is written once, final.
		r := row{Profile: l.Profile, Generation: l.Generation, QID: seq + 1, Lead: it.Lead, Class: it.Class,
			Bytes: int64(len(it.Payload)), Payload: string(it.Payload), SHA: sha256.Sum256(it.Payload),
			State: governor.QueueQueued, EnqAt: q.now().Unix()}
		mac, err := q.mac(r)
		if err != nil {
			return err
		}
		if _, err := x.ExecContext(ctx, `INSERT INTO doomsday_queue(qid,profile,generation,lead,class,bytes,payload,payload_sha,state,enq_at,mac) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			r.QID, r.Profile, r.Generation, r.Lead, string(r.Class), r.Bytes, r.Payload, r.SHA[:], string(r.State), r.EnqAt, mac); err != nil {
			return mapErr(err)
		}
		qid = r.QID
		return nil
	})
	if err != nil {
		return 0, err
	}
	return qid, nil
}

// Depth is the pending count and pending bytes.
func (q *Queue) Depth(ctx context.Context, profile string, generation int64) (int, int64, error) {
	var rows int
	var bytes int64
	err := q.db.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(bytes), 0) FROM doomsday_queue WHERE profile = ? AND generation = ? AND state = 'queued'`,
		profile, generation).Scan(&rows, &bytes)
	return rows, bytes, err
}

func validate(l governor.Limits, it governor.Item) error {
	switch {
	case l.Profile == "":
		return fmt.Errorf("%w: no profile", ErrInvalidItem)
	case it.Lead == "" || len(it.Lead) > MaxLeadBytes || !cleanText(it.Lead):
		return fmt.Errorf("%w: lead", ErrInvalidItem)
	case it.Class != governor.ClassBoss && it.Class != governor.ClassNormal:
		return fmt.Errorf("%w: class", ErrInvalidItem)
	case len(it.Payload) == 0 || !cleanText(string(it.Payload)):
		return fmt.Errorf("%w: payload must be non-empty UTF-8 text without NUL", ErrInvalidItem)
	case int64(len(it.Payload)) > l.QueueMaxBytes:
		return fmt.Errorf("%w: payload larger than the queue byte bound", governor.ErrQueueFull)
	}
	return nil
}

func cleanText(s string) bool { return utf8.ValidString(s) && !strings.ContainsRune(s, 0) }

// mapErr turns a trigger refusal into the contract's error.
func mapErr(err error) error {
	m := err.Error()
	switch {
	case strings.Contains(m, "queue is full"):
		return fmt.Errorf("%w: %v", governor.ErrQueueFull, err)
	case strings.Contains(m, "needs a running run"):
		return fmt.Errorf("%w: %v", governor.ErrNotRunning, err)
	}
	return err
}
