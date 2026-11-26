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

// KindSpec is what a kind asks of its providers' records, beyond what every
// record must be.
type KindSpec struct {
	// DefaultBaseURL completes a record added with no base_url
	// (WithKindDefaults); "" = the kind has no default.
	DefaultBaseURL string
	// NeedsKey refuses a record with no key, on this computer too.
	NeedsKey bool
}

type registeredKind struct {
	build Builder
	spec  KindSpec
}

var (
	kindsMu sync.RWMutex
	kinds   = map[string]registeredKind{}
)

// RegisterKind makes kind available to providers; an adapter calls it from
// init(), with its KindSpec if it has one. It panics on an invalid, reserved
// or duplicate kind. The returned func unregisters it (for tests:
// t.Cleanup(RegisterKind(…))).
func RegisterKind(kind string, build Builder, spec ...KindSpec) (unregister func()) {
	if !providerName.MatchString(kind) || kind == jev.ProviderTypeSafe || build == nil {
		panic(fmt.Sprintf("jevconf: RegisterKind(%q): invalid or reserved kind", kind))
	}
	kindsMu.Lock()
	defer kindsMu.Unlock()
	if _, dup := kinds[kind]; dup {
		panic(fmt.Sprintf("jevconf: RegisterKind(%q): registered twice", kind))
	}
	r := registeredKind{build: build}
	if len(spec) > 0 {
		r.spec = spec[0]
	}
	kinds[kind] = r
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
	r, ok := kinds[kind]
	return r.build, ok
}

func specFor(kind string) KindSpec {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	return kinds[kind].spec
}

// WithKindDefaults is p as SaveProvider and PutProviderKey read it: trimmed,
// and with its kind's default base_url when it names none. A caller that
// shows or compares a record before saving it uses this, so what it shows is
// what is saved. A stored record is never completed on use.
func WithKindDefaults(p Provider) Provider {
	p = normaliseProvider(p)
	if p.BaseURL == "" {
		p.BaseURL = specFor(p.Kind).DefaultBaseURL
	}
	return p
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
