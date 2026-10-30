package coderhost

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/monoes/mono-agent/internal/ai"
	"github.com/monoes/mono-agent/internal/tasks"
)

// spawnIntentsPerSweep bounds the work of one Tick.
const spawnIntentsPerSweep = 20

// SweepSpawnIntents ends the spawns whose intent is past its lease (see spawn_intent.go): at most limit of them per
// call, each scoped to its own profile, a failing one left (as 'sweeping') for the next pass. The snapshot it reads
// is only a list of candidates: every one is CLAIMED first and the sweep acts only on a claim that changed one row,
// so a spawn that finished (or a sweeper that got there first) is never undone from a stale read. It returns how
// many it ended.
func (s *Store) SweepSpawnIntents(ctx context.Context, limit int) (int, error) {
	now := s.now().UTC()
	leaseCut := now.Add(-SpawnIntentLease).Format(timeLayout)
	createCut := now.Add(-SpawnCreateLeases * SpawnIntentLease).Format(timeLayout)
	rows, err := s.db.Query(`SELECT profile_id, spawner, scope_hash, generation FROM coder_spawn_intents
		WHERE (state = 'pending' AND created_at < ? AND (child != '' OR created_at < ?)) OR state = 'sweeping' ORDER BY created_at LIMIT ?`, leaseCut, createCut, limit)
	if err != nil {
		return 0, err
	}
	var due []spawnIntent
	for rows.Next() {
		var in spawnIntent
		if err := rows.Scan(&in.profile, &in.spawner, &in.hash, &in.gen); err != nil {
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
		if err != nil {
			if first == nil {
				first = fmt.Errorf("ending the unfinished spawn of %s: %w", in.spawner, err)
			}
			continue
		}
		if ok {
			ended++
		}
	}
	return ended, first
}

// endSpawn removes what a CLAIMED spawn made and drops its intent. Every step is idempotent and the intent goes
// last, so a failure anywhere leaves it (sweeping) for the next pass. The conversation is deleted only while it is
// still the child of this spawner (a corrupt or reused value cannot name another chat).
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

// endSpawnNow is endSpawn for a spawn that failed in this process: it claims its own pending row first, so a sweep
// that already claimed it keeps the cleanup (nothing to do here), and acts on the child as written so far.
func (s *Store) endSpawnNow(ctx context.Context, in spawnIntent) error {
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping', generation = generation + 1 WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'pending'`, in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil // the sweep claimed it, or it is gone
	}
	in.gen++
	if err := s.db.QueryRow(`SELECT child FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ?`, in.profile, in.spawner, in.gen).Scan(&in.child); err != nil {
		return err
	}
	return s.endSpawn(ctx, in)
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
			if err := s.completeIntent(c, p.title, SessionSpawnRequest{Kind: p.kind, Reason: "recovered"}, SpawnFacts{}, "the host's sweep found the pair approved after an interrupted spawn", in, "sweeping"); err != nil && !errors.Is(err, errIntentGone) {
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
