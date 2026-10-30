package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/tasks"
)

// The spawn intent (coder_spawn_intents, migration 144). A session_spawn makes several things in separate
// operations: the conversation, its link and carve, the task session, and then the acceptance row of the decisions
// ledger. A process that dies in between must not leave a child nobody was told about. So the spawn writes an INTENT
// row first (one row per spawner and request, so a second spawn of the same request is refused while it exists) and
// ends it, in one transaction with the acceptance row, only when everything is in place. A row that outlives its
// lease belongs to a spawn that crashed or whose cleanup failed: the host's sweep (Tick) removes the pair it made.
//
// The sweep always REMOVES a draft pair and never completes it: the lead never learned the child's id and the
// first message was not sent (it is queued after the intent ends), so a completed child would be an idle session
// nobody addresses, still counted against max_children and the live cap. The lead repeats the request after the
// lease. One exception, fail closed the other way: a task session the operator already approved (no longer draft)
// is left alone and gets its acceptance row, so an approved session is never torn down.

// SpawnIntentLease is how long an intent may stay before the sweep treats its spawn as dead. A spawn takes
// milliseconds; the lease only has to be longer than the slowest one.
const SpawnIntentLease = 2 * time.Minute

// spawnIntentsPerSweep bounds the work of one Tick.
const spawnIntentsPerSweep = 20

// ErrSpawnPending refuses a spawn of a request whose earlier spawn has not finished (or been cleaned up).
var ErrSpawnPending = fmt.Errorf("%w: an earlier spawn of this same request is unfinished (it may have been interrupted); it is cleaned up within a few minutes, then repeat the request", ErrLimit)

// errIntentGone: the spawn's intent was ended by the host's sweep before the spawn finished.
var errIntentGone = errors.New("the spawn's intent was ended by the host's sweep before the spawn finished")

// finishSpawn is the end of an accepted spawn: the acceptance row and the end of the intent, in ONE transaction, so
// either the ledger holds the acceptance and nothing is pending, or the intent is still there for the sweep. It
// refuses when the intent is gone (the sweep removed the pair), which makes the spawn undo itself.
func (s *Store) finishSpawn(c Caller, title string, req SessionSpawnRequest, f SpawnFacts, why, hash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	if _, err := s.recordIn(tx, spawnEntry(c, title, req, f, AdviceAccept, why)); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, c.ProfileID, c.ConversationID, hash)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errIntentGone
	}
	return tx.Commit()
}

type spawnIntent struct{ profile, spawner, hash, child string }

// openSpawnIntent writes the intent; one that exists already refuses the spawn.
func (s *Store) openSpawnIntent(profile, spawner, hash string) error {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO coder_spawn_intents (profile_id, spawner, scope_hash, child, created_at) VALUES (?, ?, ?, '', ?)`, profile, spawner, hash, s.stamp())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSpawnPending
	}
	return nil
}

// setIntentChild names the conversation the spawn made, once it is linked below the spawner.
func (s *Store) setIntentChild(in spawnIntent, child string) error {
	_, err := s.db.Exec(`UPDATE coder_spawn_intents SET child = ? WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, child, in.profile, in.spawner, in.hash)
	return err
}

func (s *Store) readIntent(in spawnIntent) (spawnIntent, bool, error) {
	err := s.db.QueryRow(`SELECT child FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, in.profile, in.spawner, in.hash).Scan(&in.child)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return in, false, nil
		}
		return in, false, err
	}
	return in, true, nil
}

// SweepSpawnIntents ends the spawns whose intent is older than the lease: at most limit of them per call, each
// scoped to its own profile, an unreadable or failing one left for the next pass. It returns how many it ended.
func (s *Store) SweepSpawnIntents(ctx context.Context, limit int) (int, error) {
	cutoff := s.now().UTC().Add(-SpawnIntentLease).Format(timeLayout)
	rows, err := s.db.Query(`SELECT profile_id, spawner, scope_hash, child FROM coder_spawn_intents WHERE created_at < ? ORDER BY created_at LIMIT ?`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	var due []spawnIntent
	for rows.Next() {
		var in spawnIntent
		if err := rows.Scan(&in.profile, &in.spawner, &in.hash, &in.child); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, in)
	}
	rows.Close()
	ended := 0
	var first error
	for _, in := range due {
		if err := s.endSpawn(ctx, in); err != nil {
			if first == nil {
				first = fmt.Errorf("ending the unfinished spawn of %s: %w", in.spawner, err)
			}
			continue
		}
		ended++
	}
	return ended, first
}

// endSpawn removes what an unfinished spawn made and drops its intent. Every step is idempotent and the intent
// goes last, so a failure anywhere leaves it for the next pass. The conversation is deleted only while it is still
// the child of this spawner (a corrupt or reused value cannot name another chat).
func (s *Store) endSpawn(ctx context.Context, in spawnIntent) error {
	if in.child != "" {
		adopted, err := s.stopDraftPair(ctx, in)
		if err != nil {
			return err
		}
		if adopted {
			return s.dropIntent(in) // the operator approved it: it stays, with its acceptance row
		}
		if x, ok, err := s.GetSession(in.profile, in.child); err != nil {
			return err
		} else if ok && x.Role == "child" && x.Parent == in.spawner {
			if err := s.deleteIfNotOwnedByAnother(in.profile, 0, 0, 0, in.child); err != nil && !errors.Is(err, ai.ErrConversationNotFound) {
				return err
			}
		}
	}
	return s.dropIntent(in)
}

// endSpawnNow is endSpawn for a spawn that failed in this process: it reads the intent's child as written so far.
func (s *Store) endSpawnNow(ctx context.Context, in spawnIntent) error {
	cur, ok, err := s.readIntent(in)
	if err != nil {
		return err
	}
	if !ok {
		return nil // already ended
	}
	return s.endSpawn(ctx, cur)
}

func (s *Store) dropIntent(in spawnIntent) error {
	_, err := s.db.Exec(`DELETE FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, in.profile, in.spawner, in.hash)
	return err
}

// stopDraftPair stops the draft task session(s) the spawn created for the child conversation (a task session of the
// spawner's own session, linked to the child). A session no longer draft was approved by the operator: it is left
// running and gets the acceptance row that was missing (adopted = true).
func (s *Store) stopDraftPair(ctx context.Context, in spawnIntent) (adopted bool, err error) {
	rows, err := s.db.Query(`SELECT id, state, title, kind FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = ''
		AND parent_session_id IN (SELECT id FROM task_sessions WHERE profile_id = ? AND conversation_id = ?)`, in.profile, in.child, in.profile, in.spawner)
	if err != nil {
		return false, err
	}
	type pair struct {
		id                 int64
		state, title, kind string
	}
	var found []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.state, &p.title, &p.kind); err != nil {
			rows.Close()
			return false, err
		}
		found = append(found, p)
	}
	rows.Close()
	ts := tasks.NewStore(s.db)
	for _, p := range found {
		switch tasks.SessionState(p.state) {
		case tasks.SessionDraft:
			if _, err := ts.StopSession(ctx, in.profile, p.id, leadActor(in.spawner, p.id, in.child)); err != nil {
				return false, fmt.Errorf("stopping the draft task session #%d: %w", p.id, err)
			}
		case tasks.SessionRunning, tasks.SessionPaused:
			c := Caller{ProfileID: in.profile, ConversationID: in.spawner}
			if err := s.finishSpawn(c, p.title, SessionSpawnRequest{Kind: p.kind, Reason: "recovered"}, SpawnFacts{}, "the host's sweep found the pair approved after an interrupted spawn", in.hash); err != nil && !errors.Is(err, errIntentGone) {
				return false, err
			}
			adopted = true
		}
	}
	return adopted, nil
}

// sweepSpawnIntents is the host's pass: it runs on every Tick, bounded, and never stops the tick.
func (h *Host) sweepSpawnIntents() {
	if _, err := h.Store.SweepSpawnIntents(context.Background(), spawnIntentsPerSweep); err != nil {
		log.Printf("coder host: spawn intent sweep: %v", err)
	}
}
