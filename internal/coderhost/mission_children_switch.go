package coderhost

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The operator's switch for mission child sessions. Absent means off, on a fresh install and after an upgrade
// alike: a master whose max_children was set long ago for ordinary children must not start spawning budgeted
// child sessions because a mark (which a model may write) appeared. The setting is one row of the configs table
// under a name internal/operator reserves, so `config set` and the agents' write paths refuse it; only
// `coder sessions mission-children enable|disable` (an operator-only command) writes it, and no MCP tool does.
//
// It is read inside Reserve's transaction, the one place every spawn passes, so no entry point bypasses it.
// Turning it off stops new spawns only: a running child keeps running, and the host pass still settles, charges
// and revokes it.

// MissionChildrenConfig is the reserved configs row holding the switch ("true" = on, anything else or no row = off).
const MissionChildrenConfig = "coder.mission_children_enabled"

// ErrChildrenOff: a child session was asked for while the operator's switch is off.
var ErrChildrenOff = fmt.Errorf("%w: mission child sessions are off (operator setting): the person enables them with `coder sessions mission-children enable`", ErrInvalid)

func childrenEnabledIn(q querier) (bool, error) {
	var raw string
	err := q.QueryRow(`SELECT config_data FROM configs WHERE name = ?`, MissionChildrenConfig).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(raw) == "true", nil
}

// MissionChildrenEnabled says whether the operator allowed mission child sessions (false when never set).
func (s *Store) MissionChildrenEnabled() (bool, error) { return childrenEnabledIn(s.db) }

// SetMissionChildrenEnabled writes the switch. It is for the operator-only CLI command; the caller has checked
// that the process is the operator's.
func (s *Store) SetMissionChildrenEnabled(on bool) error {
	v := "false"
	if on {
		v = "true"
	}
	_, err := s.db.Exec(`INSERT INTO configs (name, config_data, created_at, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET config_data = excluded.config_data, updated_at = excluded.updated_at`, MissionChildrenConfig, v)
	return err
}

func (s *Store) childrenSwitchText() (string, error) {
	on, err := s.MissionChildrenEnabled()
	if err != nil {
		return "", err
	}
	if on {
		return "children: on", nil
	}
	return "children: off (operator setting)", nil
}
