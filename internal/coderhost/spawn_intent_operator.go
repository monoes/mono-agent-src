package coderhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

var (
	ErrSpawnIntentUnknown = errors.New("coder host: no such spawn intent")
	ErrSpawnIntentNotHeld = errors.New("coder host: the spawn intent is not held for the operator")
	ErrSpawnIntentStale   = errors.New("coder host: the spawn intent changed since it was listed")
	ErrSpawnIntentLive    = errors.New("coder host: the spawn's task session is no longer a draft")
)

// SpawnIntentView is one row of coder_spawn_intents as the operator sees it.
type SpawnIntentView struct {
	Spawner       string  `json:"spawner"`
	State         string  `json:"state"`
	Generation    int64   `json:"generation"`
	Attempts      int64   `json:"attempts"`
	CreatedAt     string  `json:"createdAt"`
	ClaimedAt     string  `json:"claimedAt"`
	Child         string  `json:"child"`
	TaskSessions  []int64 `json:"taskSessions"`
	NeedsOperator bool    `json:"needsOperator"`
}

// ListSpawnIntents lists the profile's intents with the generation a resolve must name.
func (s *Store) ListSpawnIntents(profile string) ([]SpawnIntentView, error) {
	rows, err := s.db.Query(`SELECT spawner, state, generation, attempts, created_at, claimed_at, child FROM coder_spawn_intents WHERE profile_id = ? ORDER BY created_at, spawner`, profile)
	if err != nil {
		return nil, err
	}
	var out []SpawnIntentView
	for rows.Next() {
		var v SpawnIntentView
		if err := rows.Scan(&v.Spawner, &v.State, &v.Generation, &v.Attempts, &v.CreatedAt, &v.ClaimedAt, &v.Child); err != nil {
			rows.Close()
			return nil, err
		}
		v.NeedsOperator, v.TaskSessions = v.State == "needs-operator", []int64{}
		out = append(out, v)
	}
	rows.Close()
	for i := range out {
		if out[i].Child == "" {
			continue
		}
		ids, err := s.db.Query(`SELECT id FROM task_sessions WHERE profile_id = ? AND conversation_id = ? AND archived_at = ''`, profile, out[i].Child)
		if err != nil {
			return nil, err
		}
		for ids.Next() {
			var id int64
			if err := ids.Scan(&id); err == nil {
				out[i].TaskSessions = append(out[i].TaskSessions, id)
			}
		}
		ids.Close()
	}
	return out, nil
}

// CountHeldSpawnIntents counts the intents held for the operator, in every profile (the doctor's signal).
func (s *Store) CountHeldSpawnIntents() (n int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*) FROM coder_spawn_intents WHERE state = 'needs-operator'`).Scan(&n)
	return n, err
}

// ResolveSpawnIntent ends a needs-operator intent at the generation `list` showed, and no other row. keep writes ONE
// acceptance row (the spawn is accepted as it stands) and drops the intent in one transaction, leaving the child and
// its task session alone. Discard claims the row with a guarded UPDATE like the sweep's and runs the sweep's own
// cleanup, which removes only a draft pair: a task session that is no longer a draft refuses the discard and the row
// stays as it was.
func (s *Store) ResolveSpawnIntent(ctx context.Context, profile, spawner string, gen int64, keep bool) error {
	var in spawnIntent
	var state string
	err := s.db.QueryRow(`SELECT scope_hash, child, state, generation, attempts FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ?`, profile, spawner).Scan(&in.hash, &in.child, &state, &in.gen, &in.attempts)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrSpawnIntentUnknown
	case err != nil:
		return err
	case state != "needs-operator":
		return fmt.Errorf("%w: it is %s", ErrSpawnIntentNotHeld, state)
	case in.gen != gen:
		return ErrSpawnIntentStale
	}
	in.profile, in.spawner = profile, spawner
	if keep {
		return s.keepIntent(in)
	}
	if live, err := s.liveSessions(in); err != nil {
		return err
	} else if live {
		return ErrSpawnIntentLive
	}
	res, err := s.db.Exec(`UPDATE coder_spawn_intents SET state = 'sweeping', generation = generation + 1, claimed_at = ?, attempts = attempts + 1 WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'needs-operator'`, s.stamp(), profile, spawner, gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSpawnIntentStale
	}
	in.gen++
	if err := s.endSpawn(ctx, in); errors.Is(err, errHeld) {
		return ErrSpawnIntentLive
	} else {
		return err
	}
}

// keepIntent is the operator's acceptance: the intent row and the ledger row in ONE transaction.
func (s *Store) keepIntent(in spawnIntent) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // after commit it is a no-op
	res, err := tx.Exec(`DELETE FROM coder_spawn_intents WHERE profile_id = ? AND spawner = ? AND generation = ? AND state = 'needs-operator'`, in.profile, in.spawner, in.gen)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrSpawnIntentStale
	}
	if _, err := s.recordIn(tx, operatorKeepEntry(in.profile, in.spawner, in.child, in.gen)); err != nil {
		return err
	}
	return tx.Commit()
}
