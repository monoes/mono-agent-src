package agentpurposes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// ConfigName is the configs-table row holding the purposes. It is a reserved row
// (internal/operator): `config set` refuses it, and `agent purposes set|reset`, which only the
// operator may run, are the writers.
const ConfigName = "agent.purposes"

type stored struct {
	V        int                 `json:"v"`
	Purposes map[string]Settings `json:"purposes"`
}

// Load reads the purposes. No row is an empty set. A row that cannot be read, names a purpose that is
// not in the catalog or holds settings that do not validate is an error, never an empty set: the
// operator's restriction would be unknown, and a spawn must not run unrestricted by mistake.
func Load(ctx context.Context, db *sql.DB) (Set, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT config_data FROM configs WHERE name = ?`, ConfigName).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Set{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the agent purposes: %w", err)
	}
	var st stored
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, fmt.Errorf("the agent purposes row is unreadable: %w", err)
	}
	set := Set{}
	for id, s := range st.Purposes {
		if _, ok := Find(id); !ok {
			return nil, fmt.Errorf("the agent purposes row names %q, which is not a purpose", id)
		}
		s = s.Normalize()
		if err := s.Validate(); err != nil {
			return nil, fmt.Errorf("the agent purposes row holds invalid settings for %s: %w", id, err)
		}
		set[id] = s
	}
	return set, nil
}

// Save stores the purposes, or removes the row when the set is empty. Only the commands made for
// the operator call it.
func Save(ctx context.Context, db *sql.DB, set Set) error {
	if len(set) == 0 {
		if _, err := db.ExecContext(ctx, `DELETE FROM configs WHERE name = ?`, ConfigName); err != nil {
			return fmt.Errorf("clearing the agent purposes: %w", err)
		}
		return nil
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	st := stored{V: 1, Purposes: map[string]Settings{}}
	for _, id := range ids {
		if _, ok := Find(id); !ok {
			return fmt.Errorf("%q is not a purpose", id)
		}
		s := set[id].Normalize()
		if err := s.Validate(); err != nil {
			return fmt.Errorf("%s: %w", id, err)
		}
		st.Purposes[id] = s
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO configs (name, config_data, created_at, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = CURRENT_TIMESTAMP`, ConfigName, string(data)); err != nil {
		return fmt.Errorf("saving the agent purposes: %w", err)
	}
	return nil
}
