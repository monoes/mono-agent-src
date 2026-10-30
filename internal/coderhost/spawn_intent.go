package coderhost

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"time"
)

// The spawn intent (coder_spawn_intents, migration 144). A session_spawn makes several things in separate
// operations: the conversation, its link and carve, the task session, and then the acceptance row of the decisions
// ledger. A process that dies in between must not leave a child nobody was told about. So the spawn writes an INTENT
// row first (one row per spawner, so a second spawn of that lead is refused while it exists) and ends it, in one
// transaction with the acceptance row, only when everything is in place. A row that outlives its lease belongs to a
// spawn that crashed or whose cleanup failed: the host's sweep (Tick) removes the pair it made.
//
// Who owns an intent is decided by its generation, never by a read. The spawn is created with one; the sweep (and a
// spawn that undoes itself) CLAIMS the row with a single guarded UPDATE that sets state='sweeping' and bumps the
// generation, and acts only if that UPDATE changed one row. The spawn's own writes (its child, its finish) are
// guarded by its generation and state='pending', so after a claim the spawn can no longer finish and the sweep can no
// longer claim a spawn that has finished. Only one intent exists per spawner, whatever the request.
//
// The sweep always REMOVES a draft pair and never completes it: the lead never learned the child's id and the
// first message was not sent (it is queued after the intent ends), so a completed child would be an idle session
// nobody addresses, still counted against max_children and the live cap. The lead repeats the request after the
// lease. finishSpawn is the single owner of the acceptance row. Fail closed the other way: a task session that is no
// longer a draft (the operator approved it while the spawn was slow) is never stopped, adopted or deleted, by the
// sweep or by the spawn's own undo; the intent becomes 'needs-operator' (it still blocks further spawns of that lead,
// and the host logs it once) until a person looks at it.
//
// A claim is a lease: a 'sweeping' row is claimed again only after SpawnSweepLease (doubling with every attempt), and
// every side-effecting cleanup step is fenced on the generation of the claim it belongs to.

// SpawnIntentLease is how long an intent may stay before the sweep treats its spawn as dead. A spawn takes
// milliseconds; the lease only has to be longer than the slowest one. An intent whose child is still unnamed
// (the spawn has not created the conversation yet, or died just after) waits SpawnCreateLeases leases: dropping it
// early could leave a conversation the intent never learned of, so the longer wait is the simple safe choice
// (the alternative, searching for fresh child sessions of the spawner with no task session, would guess: it could
// take a plain child the lead made another way; the residual is written down in SECURITY.md).
const (
	SpawnIntentLease  = 2 * time.Minute
	SpawnCreateLeases = 3

	// A claimed ('sweeping') row is claimed again only after SpawnSweepLease << (claims-1) (2, 4, 8, 16 minutes: a
	// lease longer than any cleanup, then a back-off); after SpawnSweepAttempts claims it is held for the operator.
	SpawnSweepLease    = 2 * time.Minute
	SpawnSweepAttempts = 5
)

// ErrSpawnPending refuses a spawn while an earlier spawn of this lead has not finished (or been cleaned up).
var ErrSpawnPending = fmt.Errorf("%w: an earlier spawn of yours is unfinished (it may have been interrupted); it is cleaned up within a few minutes, then repeat the request", ErrLimit)

// errIntentGone: the spawn's intent was claimed by the host's sweep (or removed) before the spawn finished.
var errIntentGone = errors.New("the spawn's intent was ended by the host's sweep before the spawn finished")

type spawnIntent struct {
	profile, spawner, hash, child string
	gen, attempts                 int64
}

// newGeneration is the unpredictable first generation of an intent: a replacement intent for the same spawner never
// carries the number of the one it replaced, so a late write of the old spawn cannot match it.
func newGeneration() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b[:])>>2) + 1, nil
}

// finishSpawn is the end of an accepted spawn: the end of the intent and the acceptance row, in ONE transaction.
// It succeeds only while the row still has this spawn's generation and is pending; otherwise the sweep has claimed
// it, nothing is written to the ledger and the spawn undoes itself.
func (s *Store) finishSpawn(c Caller, title string, req SessionSpawnRequest, f SpawnFacts, why string, in spawnIntent) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`DELETE FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'pending'`, in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errIntentGone
	}
	if _, err := s.recordIn(tx, spawnEntry(c, title, req, f, AdviceAccept, why)); err != nil {
		return err
	}
	return tx.Commit()
}

// openSpawnIntent writes the intent and returns it with its generation; any intent of this spawner that exists
// already refuses the spawn.
func (s *Store) openSpawnIntent(profile, spawner, hash string) (spawnIntent, error) {
	gen, err := newGeneration()
	if err != nil {
		return spawnIntent{}, err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, state, generation, created_at) VALUES (?, ?, ?, '', 'pending', ?, ?)`, profile, spawner, hash, gen, s.stamp())
	if err != nil {
		return spawnIntent{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return spawnIntent{}, ErrSpawnPending
	}
	return spawnIntent{profile: profile, spawner: spawner, hash: hash, gen: gen}, nil
}

// setIntentChild names the conversation the spawn made, once it is linked below the spawner. It fails when the row
// is no longer this spawn's pending intent, which makes the spawn undo itself.
func (s *Store) setIntentChild(in spawnIntent, child string) error {
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET child = ? WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'pending'`, child, in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errIntentGone
	}
	return nil
}

// claimIntent takes the row with one guarded UPDATE: it must have the generation the caller saw and be claimable
// (pending and past its lease; or sweeping, a retry, whose own lease - SpawnSweepLease doubled per attempt - has run
// out). ok is false when somebody else changed or ended the row. The returned intent carries the new generation, the
// attempt count and the child as it is now.
func (s *Store) claimIntent(in spawnIntent, leaseCut, createCut string) (spawnIntent, bool, error) {
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping', generation = generation + 1, claimed_at = ?, attempts = attempts + 1
		WHERE profile_id = ? AND spawner = ? AND scope_hash = ? AND generation = ?
		AND ((state = 'pending' AND created_at < ? AND (child != '' OR created_at < ?)) OR (state = 'sweeping' AND claimed_at < ?))`,
		s.stamp(), in.profile, in.spawner, in.hash, in.gen, leaseCut, createCut, s.sweepCut(in.attempts))
	if err != nil {
		return in, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return in, false, nil
	}
	in.gen++
	err = s.db.QueryRow(`SELECT child, attempts FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ?`, in.profile, in.spawner, in.gen).Scan(&in.child, &in.attempts)
	return in, err == nil, err
}

// sweepCut is the claimed_at before which a sweeping row that was claimed attempts times may be claimed again.
func (s *Store) sweepCut(attempts int64) string {
	return s.now().UTC().Add(-SpawnSweepLease << min(max(attempts-1, 0), SpawnSweepAttempts)).Format(timeLayout)
}

// stillClaimed fences a cleanup step: the claim the step belongs to (its generation) must still be the row's.
func (s *Store) stillClaimed(in spawnIntent) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'sweeping'`, in.profile, in.spawner, in.gen).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errIntentGone
	}
	return nil
}

// holdForOperator ends the claim without touching anything else: the row stays, blocking the lead, for a person.
func (s *Store) holdForOperator(in spawnIntent, why string) error {
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET state = 'needs-operator', generation = generation + 1 WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'sweeping'`, in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errIntentGone
	}
	log.Printf("coder host: the spawn of %s (child %q) needs the operator: %s; the lead is blocked from spawning until the intent row of coder_spawn_intents is looked at", in.spawner, in.child, why)
	return nil
}

// dropIntent ends a claimed intent (the generation is the claim's).
func (s *Store) dropIntent(in spawnIntent) error {
	_, err := s.db.Exec(`DELETE FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'sweeping'`, in.profile, in.spawner, in.gen)
	return err
}
