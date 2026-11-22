package jevconf

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/monoes/mono-agent/internal/jev"
)

// The registry of provider kinds and the storage of provider records.

// Builder makes a provider's backend from its record and its key ("" for a keyless provider).
type Builder func(p Provider, key string) (jev.Backend, error)

var (
	kindsMu sync.RWMutex
	kinds   = map[string]Builder{}
)

// RegisterKind makes kind available to providers; an adapter calls it from
// init(). It panics on an invalid, reserved or duplicate kind. The returned
// func unregisters it (for tests: t.Cleanup(RegisterKind(…))).
func RegisterKind(kind string, build Builder) (unregister func()) {
	if !providerName.MatchString(kind) || kind == jev.ProviderTypeSafe || build == nil {
		panic(fmt.Sprintf("jevconf: RegisterKind(%q): invalid or reserved kind", kind))
	}
	kindsMu.Lock()
	defer kindsMu.Unlock()
	if _, dup := kinds[kind]; dup {
		panic(fmt.Sprintf("jevconf: RegisterKind(%q): registered twice", kind))
	}
	kinds[kind] = build
	return func() {
		kindsMu.Lock()
		delete(kinds, kind)
		kindsMu.Unlock()
	}
}

// Kinds lists the registered kinds, sorted ("typesafe" is not among them).
func Kinds() []string {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	out := make([]string, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func builderFor(kind string) (Builder, bool) {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	b, ok := kinds[kind]
	return b, ok
}

func savedProviders(db *sql.DB) ([]Provider, error) {
	raw, ok := getSetting(db, providersKey)
	if !ok {
		return nil, nil
	}
	return decodeProviders(raw)
}

func decodeProviders(raw string) ([]Provider, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []Provider
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("jev: the %s setting is unreadable: %v", providersKey, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// orgDeciderUses lists the orgs whose jev decider names provider, as
// "org decider of <profile>/<org>". It reads org_autonomy directly
// (internal/orgdecide imports this package): a database without that table
// has no org using it, and a row whose decider_json does not parse is not a
// use.
func orgDeciderUses(ctx context.Context, tx *sql.Tx, provider string) ([]string, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'org_autonomy'`).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT profile_id, org_name, decider_json FROM org_autonomy ORDER BY profile_id, org_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var uses []string
	for rows.Next() {
		var profile, org string
		var raw sql.NullString
		if err := rows.Scan(&profile, &org, &raw); err != nil {
			return nil, err
		}
		var d struct {
			Provider string `json:"provider"`
		}
		if json.Unmarshal([]byte(raw.String), &d) == nil && strings.TrimSpace(d.Provider) == provider {
			uses = append(uses, "org decider of "+profile+"/"+org)
		}
	}
	return uses, rows.Err()
}

// updateProviders reads, changes and writes the configured providers in one transaction.
func updateProviders(ctx context.Context, db *sql.DB, change func(tx *sql.Tx, saved []Provider) ([]Provider, error)) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, providersKey).Scan(&raw); err != nil && err != sql.ErrNoRows {
		return err
	}
	saved, err := decodeProviders(raw)
	if err != nil {
		return err
	}
	next, err := change(tx, saved)
	if err != nil {
		return err
	}
	sort.Slice(next, func(i, j int) bool { return next[i].Name < next[j].Name })
	enc, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, providersKey, string(enc)); err != nil {
		return err
	}
	return tx.Commit()
}
