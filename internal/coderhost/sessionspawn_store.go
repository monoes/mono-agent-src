package coderhost

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/monoes/mono-agent/internal/tasks"
)

// The store half of session_spawn: the facts the advisory reads, the memory of its one refusal (coder_spawn_advice,
// migration 144), and the decisions ledger rows.

// metaPrefix starts the plan meta line of a mission task's notes (internal/dynorg: MetaPrefix). dynorg cannot be
// imported here (it must not import the host), so the format is read by this copy, which a test in dynorg keeps equal.
const metaPrefix = "[mission-meta] "

// planMeta is the meta line's content.
type planMeta struct {
	Files []string `json:"files"`
	Size  string   `json:"size"`
	Risk  string   `json:"risk"`
}

// ParsePlanMeta reads the meta line of a task's notes (its last line); ok is false when there is none.
func ParsePlanMeta(notes string) (files []string, size, risk string, ok bool) {
	lines := strings.Split(strings.TrimRight(notes, "\n "), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, metaPrefix) {
		return nil, "", "", false
	}
	var m planMeta
	if json.Unmarshal([]byte(strings.TrimPrefix(last, metaPrefix)), &m) != nil {
		return nil, "", "", false
	}
	return m.Files, m.Size, m.Risk, true
}

// scopeFacts reads the facts of the requested scope from the tasks themselves: each id must be a task of the
// spawner's own session, and only the open ones count. What the request says about them is never used.
func (s *Store) scopeFacts(ctx context.Context, ts *tasks.Store, profile string, sid int64, actor tasks.Actor, ids []int64) (SpawnFacts, error) {
	if len(ids) == 0 {
		return FactsOf(nil), nil
	}
	plan, err := ts.PlanOfSession(ctx, profile, sid, actor)
	if err != nil {
		return SpawnFacts{}, errMissionRefused
	}
	byID := make(map[int64]tasks.Task, len(plan.Tasks))
	for _, t := range plan.Tasks {
		byID[t.ID] = t
	}
	var scope []ScopeTask
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		t, ok := byID[id]
		if !ok {
			return SpawnFacts{}, errMissionRefused // not a task of the spawner's session, or no task at all: one answer
		}
		if t.Status == tasks.StatusDone || t.Status == tasks.StatusArchived || t.Kind == tasks.KindEpic {
			continue
		}
		files, size, risk, _ := ParsePlanMeta(t.Notes)
		scope = append(scope, ScopeTask{ID: t.ID, Size: size, Risk: risk, Writes: len(files) > 0, DependsOn: t.DependsOn})
	}
	return FactsOf(scope), nil
}

// advisedBefore says whether the advisory already refused this request of this spawner.
func (s *Store) advisedBefore(profile, spawner, hash string) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM coder_spawn_advice WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, profile, spawner, hash).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// MaxAdvisedPerSpawner bounds the remembered refusals of one spawner.
const MaxAdvisedPerSpawner = 50

// markAdvised remembers the refusal. A spawner keeps at most MaxAdvisedPerSpawner of them (the oldest go), so a loop of
// new requests cannot grow the table.
func (s *Store) markAdvised(profile, spawner, hash string) error {
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO coder_spawn_advice (profile_id, spawner, scope_hash, created_at) VALUES (?, ?, ?, ?)`, profile, spawner, hash, s.stamp()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM coder_spawn_advice WHERE profile_id = ? AND spawner = ? AND scope_hash NOT IN
		(SELECT scope_hash FROM coder_spawn_advice WHERE profile_id = ? AND spawner = ? ORDER BY created_at DESC, rowid DESC LIMIT ?)`,
		profile, spawner, profile, spawner, MaxAdvisedPerSpawner)
	return err
}

// clearAdvised forgets the refusal once the repeated request was carried out.
func (s *Store) clearAdvised(profile, spawner, hash string) error {
	_, err := s.db.Exec(`DELETE FROM coder_spawn_advice WHERE profile_id = ? AND spawner = ? AND scope_hash = ?`, profile, spawner, hash)
	return err
}
