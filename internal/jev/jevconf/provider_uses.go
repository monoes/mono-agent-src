package jevconf

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Who uses a provider: the settings that choose it, and the org deciders
// that name it or follow the default. org_autonomy is read directly
// (internal/orgdecide imports this package): a database without that table
// has no org, and a row whose decider_json does not parse is not a use.

// DeciderKindJev is orgdesign.DeciderJev: the decider kind that asks a
// decision provider (a test in orgdecide holds the two equal).
const DeciderKindJev = "jev"

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ProviderUses lists what chooses provider by name: the machine default
// ("jev.provider"), a profile's surface ("jev.<profile>.<surface>.provider")
// and an org's jev decider ("org decider of <profile>/<org>"). It does not
// list what follows the default (DeciderFollowers).
func ProviderUses(ctx context.Context, db *sql.DB, provider string) ([]string, error) {
	return providerUses(ctx, db, provider)
}

func providerUses(ctx context.Context, q queryer, provider string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT key FROM settings WHERE value = ? AND (key = ? OR key LIKE 'jev.%.provider') ORDER BY key`,
		provider, defaultProviderKey)
	if err != nil {
		return nil, err
	}
	var uses []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		uses = append(uses, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	deciders, err := jevDeciders(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, d := range deciders {
		if d.provider == provider {
			uses = append(uses, "org decider of "+d.profile+"/"+d.org)
		}
	}
	return uses, nil
}

// DeciderFollowers lists the orgs whose jev decider names no provider and
// whose profile's decider surface resolves to provider (its own choice, else
// the machine default), as "<profile>/<org>".
func DeciderFollowers(ctx context.Context, db *sql.DB, provider string) ([]string, error) {
	deciders, err := jevDeciders(ctx, db)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range deciders {
		if d.provider != "" {
			continue
		}
		if p, err := activeProvider(db, d.profile, Decider); err == nil && p == provider {
			out = append(out, d.profile+"/"+d.org)
		}
	}
	return out, nil
}

type jevDecider struct{ profile, org, provider string }

// jevDeciders lists the org deciders of kind jev, by profile and org.
func jevDeciders(ctx context.Context, q queryer) ([]jevDecider, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'org_autonomy'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT profile_id, org_name, decider_json FROM org_autonomy ORDER BY profile_id, org_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jevDecider
	for rows.Next() {
		var profile, org string
		var raw sql.NullString
		if err := rows.Scan(&profile, &org, &raw); err != nil {
			return nil, err
		}
		var d struct {
			Kind     string `json:"kind"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal([]byte(raw.String), &d) == nil && strings.TrimSpace(d.Kind) == DeciderKindJev {
			out = append(out, jevDecider{profile, org, strings.TrimSpace(d.Provider)})
		}
	}
	return out, rows.Err()
}

// SurfaceProviderSetting is the provider profileID chose for surface s
// itself (set false when s follows the machine default).
func SurfaceProviderSetting(db *sql.DB, profileID string, s Surface) (name string, set bool) {
	key, err := settingKey(profileID, s, "provider")
	if err != nil {
		return "", false
	}
	v, err := providerSetting(db, key)
	return v, err == nil && v != ""
}
