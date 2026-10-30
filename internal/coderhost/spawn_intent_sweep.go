package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/tasks"
)

// spawnIntentsPerSweep bounds the work of one Tick.
const spawnIntentsPerSweep = 20

// errHeld: the claim ended by holding the intent for the operator (nothing was removed).
var errHeld = errors.New("the spawn was held for the operator")

// SweepSpawnIntents ends the spawns whose intent is past its lease (see spawn_intent.go): at most limit of them per
// call, each scoped to its own profile, a failing one left (as 'sweeping', claimed again after a back-off, held for the
// operator after SpawnSweepAttempts) for a later pass. The snapshot it reads is only a list of candidates: every one is
// CLAIMED first and the sweep acts only on a claim that changed one row, so a spawn that finished (or a sweeper that
// got there first, or one still inside its lease) is never undone from a stale read. It returns how many it ended.
func (s *Store) SweepSpawnIntents(ctx context.Context, limit int) (int, error) {
	now := s.now().UTC()
	leaseCut := now.Add(-SpawnIntentLease).Format(timeLayout)
	createCut := now.Add(-SpawnCreateLeases * SpawnIntentLease).Format(timeLayout)
	rows, err := s.db.Query(`SELECT profile_id, spawner, scope_hash, generation, attempts FROM coder_spawn_intents
		WHERE (state = 'pending' AND created_at < ? AND (child != '' OR created_at < ?)) OR (state = 'sweeping' AND claimed_at < ?) ORDER BY created_at LIMIT ?`,
		leaseCut, createCut, s.sweepCut(0), limit)
	if err != nil {
		return 0, err
	}
	var due []spawnIntent
	for rows.Next() {
		var in spawnIntent
		if err := rows.Scan(&in.profile, &in.spawner, &in.hash, &in.gen, &in.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		due = append(due, in)
	}
	rows.Close()
	if s.afterSweepRead != nil {
		s.afterSweepRead()
	}
	ended := 0
	var first error
	for _, in := range due {
		claimed, ok, err := s.claimIntent(in, leaseCut, createCut)
		if err == nil && ok {
			err = s.endSpawn(ctx, claimed)
		}
		switch {
		case err == nil && ok:
			ended++
		case err == nil, errors.Is(err, errIntentGone), errors.Is(err, errHeld):
		default:
			if ok && claimed.attempts >= SpawnSweepAttempts {
				_ = s.holdForOperator(claimed, fmt.Sprintf("the cleanup failed %d times, last: %v", claimed.attempts, err))
				continue
			}
			if first == nil {
				first = fmt.Errorf("ending the unfinished spawn of %s: %w", in.spawner, err)
			}
		}
	}
	return ended, first
}

// endSpawn removes what a CLAIMED spawn made and drops its intent. Every step is idempotent, fenced on the claim's
// generation, and the intent goes last, so a failure anywhere leaves it (sweeping) for a later pass. The conversation
// is deleted only while it is still the child of this spawner (a corrupt or reused value cannot name another chat)
// and only while its task session is a draft: anything else is held for the operator and nothing is touched.
func (s *Store) endSpawn(ctx context.Context, in spawnIntent) error {
	if in.child != "" {
		if err := s.stillClaimed(in); err != nil {
			return err
		}
		live, err := s.stopDraftPair(ctx, in)
		if err != nil {
			return err
		}
		if live {
			return s.hold(in)
		}
		if x, ok, err := s.GetSession(in.profile, in.child); err != nil {
			return err
		} else if ok && x.Role == "child" && x.Parent == in.spawner {
			err := s.deleteIfNotOwnedByAnotherAnd(in.profile, 0, 0, 0, in.child, func(ctx context.Context, c *sql.Conn) (bool, error) {
				var mine, notDraft int
				err := c.QueryRowContext(ctx, `SELECT
					(SELECT COUNT(*) FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'sweeping'),
					(SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state NOT IN ('draft', 'stopped'))`,
					in.profile, in.spawner, in.gen, in.profile, in.child).Scan(&mine, &notDraft)
				return mine == 1 && notDraft == 0, err
			})
			switch {
			case err == nil, errors.Is(err, ai.ErrConversationNotFound):
			case errors.Is(err, ai.ErrConversationGuarded):
				if err := s.stillClaimed(in); err != nil {
					return err
				}
				if live, _ := s.liveSessions(in); live {
					return s.hold(in)
				}
				return err
			default:
				return err
			}
		}
	}
	return s.dropIntent(in)
}

// hold marks the claimed intent for the operator because its task session is no longer a draft.
func (s *Store) hold(in spawnIntent) error {
	if err := s.holdForOperator(in, "its task session is no longer a draft (the operator approved it)"); err != nil {
		return err
	}
	return errHeld
}

// endSpawnNow is endSpawn for a spawn that failed in this process: it claims its own pending row first, so a sweep
// that already claimed it keeps the cleanup (nothing to do here), and acts on the child as written so far.
func (s *Store) endSpawnNow(ctx context.Context, in spawnIntent) error {
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping', generation = generation + 1, claimed_at = ?, attempts = attempts + 1 WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'pending'`, s.stamp(), in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil // the sweep claimed it, or it is gone
	}
	in.gen++
	if err := s.db.QueryRow(`SELECT child, attempts FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ?`, in.profile, in.spawner, in.gen).Scan(&in.child, &in.attempts); err != nil {
		return err
	}
	if err := s.endSpawn(ctx, in); !errors.Is(err, errHeld) {
		return err
	}
	return nil // held: the host logged it and the intent stays for the operator
}

// liveSessions says whether the child has a task session that is not a draft (and not already stopped).
func (s *Store) liveSessions(in spawnIntent) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state NOT IN ('draft', 'stopped')`, in.profile, in.child).Scan(&n)
	return n > 0, err
}

// draftOnlyGuard is the spawn's undo guard: the conversation stays when a task session of it is no longer a draft.
func draftOnlyGuard(profile string) func(context.Context, *sql.Conn, string) (bool, error) {
	return func(ctx context.Context, c *sql.Conn, conv string) (bool, error) {
		var n int
		err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = '' AND state NOT IN ('draft', 'stopped')`, profile, conv).Scan(&n)
		return n == 0, err
	}
}

// stopDraftPair stops the draft task session(s) the spawn created for the child conversation (a task session of the
// spawner's own session, linked to the child). live is true, and nothing is stopped, when one of them is no longer a
// draft: the operator approved it, and it is never stopped or adopted here.
func (s *Store) stopDraftPair(ctx context.Context, in spawnIntent) (live bool, err error) {
	rows, err := s.db.Query(`SELECT id, state FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = ''
		AND parent_session_id IN (SELECT id FROM task_sessions WHERE profile_id = ? AND conversation_id = ?)`, in.profile, in.child, in.profile, in.spawner)
	if err != nil {
		return false, err
	}
	var drafts []int64
	for rows.Next() {
		var id int64
		var state string
		if err := rows.Scan(&id, &state); err != nil {
			rows.Close()
			return false, err
		}
		switch tasks.SessionState(state) {
		case tasks.SessionDraft:
			drafts = append(drafts, id)
		case tasks.SessionStopped:
		default:
			live = true
		}
	}
	rows.Close()
	if live {
		return true, nil
	}
	ts := tasks.NewStore(s.db)
	for _, id := range drafts {
		if err := s.stillClaimed(in); err != nil {
			return false, err
		}
		if _, err := ts.StopSession(ctx, in.profile, id, leadActor(in.spawner, id, in.child)); err != nil {
			return false, fmt.Errorf("stopping the draft task session #%d: %w", id, err)
		}
	}
	return false, nil
}

// sweepSpawnIntents is the host's pass: it runs on every Tick, bounded, and never stops the tick.
func (h *Host) sweepSpawnIntents() {
	if _, err := h.Store.SweepSpawnIntents(context.Background(), spawnIntentsPerSweep); err != nil {
		log.Printf("coder host: spawn intent sweep: %v", err)
	}
}
